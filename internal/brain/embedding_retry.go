package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pgvector/pgvector-go"
)

const (
	embeddingClaimLease     = 5 * time.Minute
	embeddingErrorRuneLimit = 2000
)

// EmbeddingRetryResult reports one durable retry pass. Failed rows remain in
// PostgreSQL with their original content and a later retry time.
type EmbeddingRetryResult struct {
	Attempted int64 `json:"attempted"`
	Indexed   int64 `json:"indexed"`
	Failed    int64 `json:"failed"`
	Pending   int64 `json:"pending"`
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
				next := time.Now().UTC().Add(embeddingRetryDelay(
					b.config.EmbeddingRetryInterval,
					b.config.EmbeddingRetryMaxInterval,
					item.Attempts,
				))
				if updateErr := b.recordEmbeddingFailure(ctx, table, item.ID, err, next); updateErr != nil {
					return result, updateErr
				}
				result.Failed++
				continue
			}

			updated, err := b.finishEmbedding(ctx, table, item.ID, vec)
			if err != nil {
				next := time.Now().UTC().Add(embeddingRetryDelay(
					b.config.EmbeddingRetryInterval,
					b.config.EmbeddingRetryMaxInterval,
					item.Attempts,
				))
				if updateErr := b.recordEmbeddingFailure(ctx, table, item.ID, err, next); updateErr != nil {
					return result, fmt.Errorf("%v; %w", err, updateErr)
				}
				result.Failed++
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
			ORDER BY COALESCE(embedding_retry_at, created_at), id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE %s AS target
		SET embedding_retry_at = $2,
			embedding_attempts = target.embedding_attempts + 1,
			embedding_updated_at = now()
		FROM candidates
		WHERE target.id = candidates.id
		RETURNING target.id, target.content, target.embedding_attempts
	`, table, table)

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
			embedding_updated_at = now()
		WHERE id = $3 AND embedding IS NULL
	`, table)
	_, err := b.pool.Exec(ctx, query, embeddingErrorText(cause), retryAt, id)
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
		if delay >= maximum/2 {
			return maximum
		}
		delay *= 2
	}
	if delay > maximum {
		return maximum
	}
	return delay
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
