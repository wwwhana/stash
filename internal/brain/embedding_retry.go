package brain

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/pgvector/pgvector-go"
)

const (
	embeddingClaimLease     = 5 * time.Minute
	embeddingErrorRuneLimit = 2000
	// A row is paused after this many failed provider attempts. The attempt
	// count includes the claim currently being processed, so this is a hard
	// cap on provider calls for one queued row until an operator wakes it.
	embeddingRetryMaxAttempts = 5
	// Positive jitter keeps retries from arriving in one burst while never
	// scheduling an attempt earlier than the exponential backoff floor.
	embeddingRetryJitterDivisor = 5
)

// EmbeddingRetryResult reports one durable retry pass. Failed rows remain in
// PostgreSQL with their original content and a later retry time, or with a
// paused marker after the hard attempt cap is reached.
type EmbeddingRetryResult struct {
	Attempted int64 `json:"attempted"`
	Indexed   int64 `json:"indexed"`
	Failed    int64 `json:"failed"`
	Pending   int64 `json:"pending"`
	// Paused counts rows that reached the hard attempt cap in this pass. They
	// remain pending until ForceRetryPendingEmbeddings or a reindex wakes them.
	Paused int64 `json:"paused,omitempty"`
	// Error contains the first bounded failure in this pass. The per-row
	// embedding_last_error column remains the detailed durable record; this
	// summary makes an aggregate warning actionable without a database query.
	Error string `json:"error,omitempty"`
}

// EmbeddingMaintenanceStatus is the operator-facing state of the durable
// embedding queue. It intentionally excludes memory content.
type EmbeddingMaintenanceStatus struct {
	Model           string `json:"model"`
	Dimensions      int    `json:"dimensions"`
	EpisodesTotal   int64  `json:"episodes_total"`
	FactsTotal      int64  `json:"facts_total"`
	EpisodesPending int64  `json:"episodes_pending"`
	FactsPending    int64  `json:"facts_pending"`
	Pending         int64  `json:"pending"`
	Due             int64  `json:"due"`
	Failed          int64  `json:"failed"`
	Paused          int64  `json:"paused,omitempty"`
	LatestError     string `json:"latest_error,omitempty"`
}

// EmbeddingRetryWake returns the notification channel used by the background
// worker. A nil channel is safe for Brain values built directly in tests.
func (b *Brain) EmbeddingRetryWake() <-chan struct{} {
	if b == nil {
		return nil
	}
	return b.embeddingRetryWake
}

// WakeEmbeddingRetries asks the background worker to run a pass immediately.
// It is deliberately non-blocking so an admin request cannot wait on a worker
// that is already processing a batch.
func (b *Brain) WakeEmbeddingRetries() {
	if b == nil || b.embeddingRetryWake == nil {
		return
	}
	select {
	case b.embeddingRetryWake <- struct{}{}:
	default:
	}
}

// EmbeddingMaintenanceStatus returns queue counts and the most recent bounded
// provider error without exposing memory content.
func (b *Brain) EmbeddingMaintenanceStatus(ctx context.Context) (EmbeddingMaintenanceStatus, error) {
	if err := b.ensureEmbeddingMaintenanceReady(); err != nil {
		return EmbeddingMaintenanceStatus{}, err
	}
	status := EmbeddingMaintenanceStatus{Model: b.embedder.Model(), Dimensions: b.embedder.Dims()}
	for _, table := range []string{"episodes", "facts"} {
		var total, pending, due, failed, paused int64
		query := fmt.Sprintf(`
			SELECT count(*) FILTER (WHERE deleted_at IS NULL),
			       count(*) FILTER (WHERE embedding IS NULL AND deleted_at IS NULL),
			       count(*) FILTER (
			           WHERE embedding IS NULL AND deleted_at IS NULL
			             AND (embedding_retry_at IS NULL OR embedding_retry_at <= now())
			             AND (embedding_lease_until IS NULL OR embedding_lease_until <= now())
			             AND embedding_attempts < %d
			       ),
			       count(*) FILTER (
			           WHERE embedding IS NULL AND deleted_at IS NULL
			             AND embedding_attempts > 0 AND embedding_last_error IS NOT NULL
			       ),
			       count(*) FILTER (
			           WHERE embedding IS NULL AND deleted_at IS NULL
			             AND embedding_attempts >= %d
			       )
			FROM %s
		`, embeddingRetryMaxAttempts, embeddingRetryMaxAttempts, table)
		if err := b.pool.QueryRow(ctx, query).Scan(&total, &pending, &due, &failed, &paused); err != nil {
			return status, fmt.Errorf("read %s embedding status: %w", table, err)
		}
		if table == "episodes" {
			status.EpisodesTotal, status.EpisodesPending = total, pending
		} else {
			status.FactsTotal, status.FactsPending = total, pending
		}
		status.Pending += pending
		status.Due += due
		status.Failed += failed
		status.Paused += paused
	}

	if err := b.pool.QueryRow(ctx, `
		SELECT coalesce((
			SELECT embedding_last_error
			FROM (
				SELECT embedding_last_error, embedding_updated_at
				FROM episodes
				WHERE embedding IS NULL AND deleted_at IS NULL AND embedding_attempts > 0 AND embedding_last_error IS NOT NULL
				UNION ALL
				SELECT embedding_last_error, embedding_updated_at
				FROM facts
				WHERE embedding IS NULL AND deleted_at IS NULL AND embedding_attempts > 0 AND embedding_last_error IS NOT NULL
			) errors
			ORDER BY embedding_updated_at DESC NULLS LAST
			LIMIT 1
		), '')
	`).Scan(&status.LatestError); err != nil {
		return status, fmt.Errorf("read latest embedding error: %w", err)
	}
	status.LatestError = embeddingErrorTextString(status.LatestError)
	return status, nil
}

// ForceRetryPendingEmbeddings makes scheduled, unleased failures eligible now.
// A worker currently holding a lease is left alone to avoid duplicate provider
// calls. The background worker is woken separately by the caller.
func (b *Brain) ForceRetryPendingEmbeddings(ctx context.Context) (int64, error) {
	if err := b.ensureEmbeddingMaintenanceReady(); err != nil {
		return 0, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin embedding retry wake: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var woken int64
	for _, table := range []string{"episodes", "facts"} {
		result, err := tx.Exec(ctx, fmt.Sprintf(`
			UPDATE %s
			SET embedding_retry_at = now(),
			    embedding_attempts = CASE
			        WHEN embedding_attempts >= %d THEN 0
			        ELSE embedding_attempts
			    END,
			    embedding_updated_at = now()
			WHERE embedding IS NULL
			  AND deleted_at IS NULL
			  AND (embedding_lease_until IS NULL OR embedding_lease_until <= now())
		`, table, embeddingRetryMaxAttempts))
		if err != nil {
			return woken, fmt.Errorf("wake %s embeddings: %w", table, err)
		}
		woken += result.RowsAffected()
	}
	if err := tx.Commit(ctx); err != nil {
		return woken, fmt.Errorf("commit embedding retry wake: %w", err)
	}
	return woken, nil
}

// QueueEmbeddingReindex clears vectors and schedules every live source row for
// a fresh vector. Source content remains untouched; this is the explicit admin
// action for a model or input-pipeline change.
func (b *Brain) QueueEmbeddingReindex(ctx context.Context) (int64, error) {
	if err := b.ensureEmbeddingMaintenanceReady(); err != nil {
		return 0, err
	}
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin embedding reindex: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var queued int64
	for _, table := range []string{"episodes", "facts"} {
		result, err := tx.Exec(ctx, fmt.Sprintf(`
			UPDATE %s
			SET embedding = NULL,
			    embedding_model = $1,
			    embedding_attempts = 0,
			    embedding_last_error = NULL,
			    embedding_retry_at = now(),
			    embedding_lease_until = NULL,
			    embedding_updated_at = now()
			WHERE deleted_at IS NULL
		`, table), b.embedder.Model())
		if err != nil {
			return queued, fmt.Errorf("queue %s reindex: %w", table, err)
		}
		queued += result.RowsAffected()
	}
	if _, err := tx.Exec(ctx, "DELETE FROM embedding_cache"); err != nil {
		return queued, fmt.Errorf("clear embedding cache: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return queued, fmt.Errorf("commit embedding reindex: %w", err)
	}
	return queued, nil
}

func embeddingErrorTextString(value string) string {
	return embeddingErrorText(fmt.Errorf("%s", value))
}

func (b *Brain) ensureEmbeddingMaintenanceReady() error {
	if b == nil || b.pool == nil || b.embedder == nil {
		return fmt.Errorf("embedding maintenance is not initialized")
	}
	return nil
}

type pendingEmbedding struct {
	ID       int64
	Content  string
	Attempts int
}

// RetryPendingEmbeddings claims due rows from both durable memory tables,
// computes their vectors, and updates only the indexing fields. Multiple Stash
// replicas can run this safely: SKIP LOCKED plus a lease prevents ordinary
// duplicate work, while a crashed worker's lease expires automatically.
func (b *Brain) RetryPendingEmbeddings(ctx context.Context, batchSize int) (EmbeddingRetryResult, error) {
	var result EmbeddingRetryResult
	if batchSize <= 0 {
		return result, fmt.Errorf("embedding retry batch size must be greater than zero")
	}

	// Reserve capacity for both queues, then give unused capacity to the queue
	// that still has work. Alternate the first queue so odd batch sizes stay fair
	// while the configured size remains a strict per-pass maximum.
	tables := []string{"episodes", "facts"}
	if b.embeddingRetryPass.Add(1)%2 == 0 {
		tables[0], tables[1] = tables[1], tables[0]
	}
	limits := []int{(batchSize + 1) / 2, batchSize / 2}
	claimed := make(map[string][]pendingEmbedding, len(tables))
	claimedTotal := 0
	for i, table := range tables {
		if limits[i] == 0 {
			continue
		}
		items, err := b.claimPendingEmbeddings(ctx, table, limits[i])
		if err != nil {
			return result, err
		}
		claimed[table] = items
		claimedTotal += len(items)
	}

	remaining := batchSize - claimedTotal
	for i, table := range tables {
		if remaining == 0 {
			break
		}
		// A short first claim means that queue was exhausted at claim time.
		if len(claimed[table]) < limits[i] {
			continue
		}
		extra, err := b.claimPendingEmbeddings(ctx, table, remaining)
		if err != nil {
			return result, err
		}
		claimed[table] = append(claimed[table], extra...)
		remaining -= len(extra)
	}

	for _, table := range tables {
		items := claimed[table]
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			result.Attempted++

			vec, err := b.embedder.Embed(ctx, item.Content)
			if err != nil {
				if result.Error == "" {
					result.Error = embeddingErrorText(err)
				}
				next, paused := embeddingRetryNextAt(
					time.Now().UTC(),
					b.config.EmbeddingRetryInterval,
					b.config.EmbeddingRetryMaxInterval,
					item.Attempts,
				)
				if updateErr := b.recordEmbeddingFailure(ctx, table, item.ID, err, next); updateErr != nil {
					return result, updateErr
				}
				result.Failed++
				if paused {
					result.Paused++
				}
				continue
			}

			updated, err := b.finishEmbedding(ctx, table, item.ID, vec)
			if err != nil {
				if result.Error == "" {
					result.Error = embeddingErrorText(err)
				}
				next, paused := embeddingRetryNextAt(
					time.Now().UTC(),
					b.config.EmbeddingRetryInterval,
					b.config.EmbeddingRetryMaxInterval,
					item.Attempts,
				)
				if updateErr := b.recordEmbeddingFailure(ctx, table, item.ID, err, next); updateErr != nil {
					return result, fmt.Errorf("%v; %w", err, updateErr)
				}
				result.Failed++
				if paused {
					result.Paused++
				}
				continue
			}
			if updated {
				result.Indexed++
			}
		}
	}

	pending, err := b.PendingEmbeddingCount(ctx)
	if err != nil {
		return result, err
	}
	result.Pending = pending
	return result, nil
}

// PendingEmbeddingCount includes rows waiting for their scheduled time as well
// as rows that are currently leased by a worker.
func (b *Brain) PendingEmbeddingCount(ctx context.Context) (int64, error) {
	var count int64
	err := b.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM episodes WHERE embedding IS NULL AND deleted_at IS NULL) +
			(SELECT count(*) FROM facts WHERE embedding IS NULL AND deleted_at IS NULL)
	`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending embeddings: %w", err)
	}
	return count, nil
}

func (b *Brain) claimPendingEmbeddings(ctx context.Context, table string, limit int) ([]pendingEmbedding, error) {
	if table != "episodes" && table != "facts" {
		return nil, fmt.Errorf("unsupported embedding table %q", table)
	}
	leaseUntil := time.Now().UTC().Add(embeddingClaimLease)
	query := fmt.Sprintf(`
			WITH candidates AS (
				SELECT id
				FROM %s
				WHERE embedding IS NULL
				  AND deleted_at IS NULL
				  AND (embedding_retry_at IS NULL OR embedding_retry_at <= now())
				  AND (embedding_lease_until IS NULL OR embedding_lease_until <= now())
				  AND embedding_attempts < %d
				ORDER BY COALESCE(embedding_retry_at, created_at), id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE %s AS target
		SET embedding_retry_at = $2,
			embedding_lease_until = $2,
			embedding_attempts = target.embedding_attempts + 1,
			embedding_updated_at = now()
		FROM candidates
		WHERE target.id = candidates.id
		RETURNING target.id, target.content, target.embedding_attempts
	`, table, embeddingRetryMaxAttempts, table)

	rows, err := b.pool.Query(ctx, query, limit, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("claim pending %s embeddings: %w", table, err)
	}
	defer rows.Close()

	items := make([]pendingEmbedding, 0, limit)
	for rows.Next() {
		var item pendingEmbedding
		if err := rows.Scan(&item.ID, &item.Content, &item.Attempts); err != nil {
			return nil, fmt.Errorf("scan pending %s embedding: %w", table, err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pending %s embeddings: %w", table, err)
	}
	return items, nil
}

func (b *Brain) finishEmbedding(ctx context.Context, table string, id int64, vec []float32) (bool, error) {
	if table != "episodes" && table != "facts" {
		return false, fmt.Errorf("unsupported embedding table %q", table)
	}
	query := fmt.Sprintf(`
		UPDATE %s
		SET embedding = $1,
			embedding_model = $2,
			embedding_last_error = NULL,
			embedding_retry_at = NULL,
			embedding_lease_until = NULL,
			embedding_updated_at = now()
		WHERE id = $3 AND embedding IS NULL
	`, table)
	tag, err := b.pool.Exec(ctx, query, pgvector.NewVector(vec), b.embedder.Model(), id)
	if err != nil {
		return false, fmt.Errorf("finish %s embedding %d: %w", table, id, err)
	}
	return tag.RowsAffected() > 0, nil
}

func (b *Brain) recordEmbeddingFailure(ctx context.Context, table string, id int64, cause error, retryAt time.Time) error {
	if table != "episodes" && table != "facts" {
		return fmt.Errorf("unsupported embedding table %q", table)
	}
	query := fmt.Sprintf(`
		UPDATE %s
		SET embedding_last_error = $1,
			embedding_retry_at = $2,
			embedding_lease_until = NULL,
			embedding_updated_at = now()
		WHERE id = $3 AND embedding IS NULL
	`, table)
	var scheduledRetry any
	if !retryAt.IsZero() {
		scheduledRetry = retryAt
	}
	_, err := b.pool.Exec(ctx, query, embeddingErrorText(cause), scheduledRetry, id)
	if err != nil {
		return fmt.Errorf("record %s embedding failure %d: %w", table, id, err)
	}
	return nil
}

func embeddingRetryDelay(base, maximum time.Duration, attempts int) time.Duration {
	if base <= 0 {
		base = time.Minute
	}
	if maximum < base {
		maximum = base
	}
	delay := base
	for i := 1; i < attempts; i++ {
		// Compare before doubling so a large configured duration or attempt
		// count cannot overflow time.Duration and produce an immediate retry.
		if delay >= maximum-delay {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
}

// embeddingRetryDelayWithJitter adds a bounded positive jitter to the
// exponential floor. The upper bound is the configured maximum, so jitter
// cannot make a retry arrive later than the operator's cap (or overflow a
// time.Duration). A separate helper keeps embeddingRetryDelay deterministic
// for callers and tests that need the exact exponential floor.
func embeddingRetryDelayWithJitter(base, maximum time.Duration, attempts int) time.Duration {
	delay := embeddingRetryDelay(base, maximum, attempts)
	if delay <= 0 || maximum <= delay {
		return delay
	}
	jitterWindow := delay / embeddingRetryJitterDivisor
	available := maximum - delay
	if jitterWindow > available {
		jitterWindow = available
	}
	if jitterWindow <= 0 {
		return delay
	}
	jitter := time.Duration(rand.Int63n(int64(jitterWindow) + 1))
	return delay + jitter
}

// embeddingRetryNextAt returns the next durable retry time and whether the
// row reached the hard attempt cap. A zero time is stored as NULL, which is
// the durable paused marker; claimPendingEmbeddings excludes capped rows.
func embeddingRetryNextAt(now time.Time, base, maximum time.Duration, attempts int) (time.Time, bool) {
	if attempts >= embeddingRetryMaxAttempts {
		return time.Time{}, true
	}
	return now.Add(embeddingRetryDelayWithJitter(base, maximum, attempts)), false
}

func embeddingErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	runes := []rune(text)
	if len(runes) > embeddingErrorRuneLimit {
		text = string(runes[:embeddingErrorRuneLimit])
	}
	return text
}
