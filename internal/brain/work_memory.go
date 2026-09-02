package brain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// RememberForWorkResult returns both the durable episode status and its typed
// link to the originating work item.
type RememberForWorkResult struct {
	RememberResult
	Link models.WorkItemMemoryLink `json:"link"`
}

const automaticWorkOutcomeLimit = resumeMemoryContentLimit

// automaticWorkOutcomeText keeps the server-side safety net small enough to
// be useful in the next resume prompt. It records the final observation, not
// the conversation that led to it.
func automaticWorkOutcomeText(summary, result, nextAction string) string {
	sections := make([]string, 0, 3)
	if value := strings.TrimSpace(summary); value != "" {
		sections = append(sections, "Summary: "+value)
	}
	if value := strings.TrimSpace(result); value != "" {
		sections = append(sections, "Result: "+value)
	}
	if value := strings.TrimSpace(nextAction); value != "" {
		sections = append(sections, "Next action: "+value)
	}
	content := strings.Join(sections, "\n")
	if len(content) <= automaticWorkOutcomeLimit {
		return content
	}
	end := automaticWorkOutcomeLimit - len("…")
	for end > 0 && !utf8.ValidString(content[:end]) {
		end--
	}
	if end <= 0 {
		return ""
	}
	return content[:end] + "…"
}

func (b *Brain) workEmbeddingModel() string {
	if b == nil || b.embedder == nil {
		return ""
	}
	return strings.TrimSpace(b.embedder.Model())
}

// saveAutomaticWorkOutcomeTx is the server-side memory safety net. Agents are
// still expected to call remember_work for decisions and lessons, but a final
// finish, handoff, or lease expiry must not discard its bounded result merely
// because the agent omitted that optional call. Embedding is deliberately
// deferred to the existing retry worker so this path never spends provider
// budget while holding the work lease transaction.
func (b *Brain) saveAutomaticWorkOutcomeTx(
	ctx context.Context,
	tx pgx.Tx,
	namespaceID, workItemID, attemptID int64,
	startedAt time.Time,
	summary, result, nextAction, reason string,
) (bool, error) {
	content := automaticWorkOutcomeText(summary, result, nextAction)
	if content == "" {
		return false, nil
	}

	var alreadyLinked bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM work_item_memory_links link
			WHERE link.work_item_id = $1 AND link.created_at >= $2
			UNION ALL
			SELECT 1
			FROM work_events event
			WHERE event.work_item_id = $1
			  AND event.event_type = 'work.memory.remembered'
			  AND event.occurred_at >= $2
		)`, workItemID, startedAt).Scan(&alreadyLinked); err != nil {
		return false, fmt.Errorf("check automatic work memory: %w", err)
	}
	if alreadyLinked {
		return false, nil
	}

	now := time.Now().UTC()
	var episodeID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO episodes (
			namespace_id, content, embedding, embedding_model, occurred_at,
			embedding_attempts, embedding_retry_at, embedding_updated_at
		) VALUES ($1, $2, NULL, $3, $4, 0, $4, now())
		RETURNING id`, namespaceID, content, b.workEmbeddingModel(), now).Scan(&episodeID); err != nil {
		return false, fmt.Errorf("insert automatic work memory: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO work_item_memory_links (work_item_id, memory_type, memory_id, relation)
		VALUES ($1, 'episode', $2, 'result')`, workItemID, episodeID); err != nil {
		return false, fmt.Errorf("link automatic work memory: %w", err)
	}
	eventKey := workActionKeyDigest(fmt.Sprintf("automatic-work-memory:%s:%d", reason, attemptID))
	if err := insertWorkExecutionEvent(ctx, tx, namespaceID, workItemID, &attemptID, "work.memory.auto_saved", eventKey, map[string]any{
		"episode_id":      episodeID,
		"relation":        "result",
		"reason":          reason,
		"indexing_status": "pending",
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (b *Brain) saveExpiredWorkOutcomeTx(ctx context.Context, tx pgx.Tx, namespaceID, workItemID, attemptID int64, startedAt time.Time) error {
	var summary, result, nextAction string
	err := tx.QueryRow(ctx, `
		SELECT summary, result, next_action
		FROM work_checkpoints
		WHERE attempt_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, attemptID).Scan(&summary, &result, &nextAction)
	if errors.Is(err, pgx.ErrNoRows) {
		summary = "Execution attempt expired"
		result = "The lease expired before a final checkpoint was recorded"
		nextAction = "Resume the work item and inspect its conditions"
	} else if err != nil {
		return fmt.Errorf("read expired work checkpoint: %w", err)
	}
	_, err = b.saveAutomaticWorkOutcomeTx(ctx, tx, namespaceID, workItemID, attemptID, startedAt, summary, result, nextAction, "expired")
	return err
}

// RememberForWork stores one durable episode and links it to a work item. The
// provider call happens before the locked transaction; the receipt is checked
// again after it so concurrent retries may repeat embedding work but never
// insert duplicate episodes.
func (b *Brain) RememberForWork(ctx context.Context, workItemID int64, content, relation, actionKey string) (*RememberForWorkResult, error) {
	content = strings.TrimSpace(content)
	if err := validateContent(content); err != nil {
		return nil, err
	}
	relation = strings.TrimSpace(relation)
	if _, ok := workMemoryRelations[relation]; !ok {
		return nil, fmt.Errorf("brain: invalid work memory relation %q", relation)
	}
	var err error
	actionKey, err = validateWorkActionKey(actionKey)
	if err != nil {
		return nil, err
	}
	requestHash, err := workActionRequestHash("remember", struct {
		WorkItemID int64  `json:"work_item_id"`
		Content    string `json:"content"`
		Relation   string `json:"relation"`
	}{workItemID, content, relation})
	if err != nil {
		return nil, err
	}

	// Avoid a provider request on ordinary retries. This first read is only an
	// optimization; the locked write transaction below is the authority.
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin work memory receipt read: %w", err)
	}
	namespaceID, _, err := lockWorkItem(ctx, tx, workItemID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	receipt, err := loadWorkActionReceipt(ctx, tx, workItemID, nil, "remember", actionKey, requestHash)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if receipt != nil {
		var replay RememberForWorkResult
		if err := decodeWorkActionResponse(receipt, &replay); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit work memory replay: %w", err)
		}
		return &replay, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit work memory receipt read: %w", err)
	}

	if b.embedder == nil {
		return nil, fmt.Errorf("brain: embedder is required for work memory")
	}
	occurredAt := time.Now().UTC()
	vector, embedErr := b.embedder.Embed(ctx, content)
	persistCtx := ctx
	var cancel context.CancelFunc
	if embedErr != nil {
		persistCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	tx, err = b.pool.Begin(persistCtx)
	if err != nil {
		return nil, fmt.Errorf("begin work memory: %w", err)
	}
	defer func() { _ = tx.Rollback(persistCtx) }()
	lockedNamespaceID, _, err := lockWorkItem(persistCtx, tx, workItemID)
	if err != nil {
		return nil, err
	}
	if lockedNamespaceID != namespaceID {
		return nil, fmt.Errorf("brain: work item namespace changed during memory write")
	}
	receipt, err = loadWorkActionReceipt(persistCtx, tx, workItemID, nil, "remember", actionKey, requestHash)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		var replay RememberForWorkResult
		if err := decodeWorkActionResponse(receipt, &replay); err != nil {
			return nil, err
		}
		if err := tx.Commit(persistCtx); err != nil {
			return nil, fmt.Errorf("commit concurrent work memory replay: %w", err)
		}
		return &replay, nil
	}

	remembered := RememberResult{}
	if embedErr == nil {
		if _, err := tx.Exec(persistCtx, "SAVEPOINT work_memory_vector"); err != nil {
			return nil, fmt.Errorf("save work memory vector point: %w", err)
		}
		err = tx.QueryRow(persistCtx,
			`INSERT INTO episodes (namespace_id, content, embedding, embedding_model, occurred_at, embedding_updated_at)
			 VALUES ($1, $2, $3, $4, $5, now()) RETURNING id`,
			namespaceID, content, pgvector.NewVector(vector), b.embedder.Model(), occurredAt,
		).Scan(&remembered.ID)
		if err == nil {
			if _, err := tx.Exec(persistCtx, "RELEASE SAVEPOINT work_memory_vector"); err != nil {
				return nil, fmt.Errorf("release work memory vector point: %w", err)
			}
			remembered.Indexed = true
			remembered.IndexingStatus = "indexed"
		} else {
			if _, rollbackErr := tx.Exec(persistCtx, "ROLLBACK TO SAVEPOINT work_memory_vector"); rollbackErr != nil {
				return nil, fmt.Errorf("store work memory vector: %v; restore transaction: %w", err, rollbackErr)
			}
			if _, releaseErr := tx.Exec(persistCtx, "RELEASE SAVEPOINT work_memory_vector"); releaseErr != nil {
				return nil, fmt.Errorf("release failed work memory vector point: %w", releaseErr)
			}
			embedErr = err
		}
	}
	if embedErr != nil {
		retryDelay := b.config.EmbeddingRetryInterval
		if retryDelay <= 0 {
			retryDelay = DefaultConfig().EmbeddingRetryInterval
		}
		retryAt := time.Now().UTC().Add(retryDelay)
		if err := tx.QueryRow(persistCtx,
			`INSERT INTO episodes (
			    namespace_id, content, embedding, embedding_model, occurred_at,
			    embedding_attempts, embedding_last_error, embedding_retry_at, embedding_updated_at
			 ) VALUES ($1, $2, NULL, $3, $4, 1, $5, $6, now()) RETURNING id`,
			namespaceID, content, b.embedder.Model(), occurredAt, embeddingErrorText(embedErr), retryAt,
		).Scan(&remembered.ID); err != nil {
			return nil, fmt.Errorf("insert pending work memory: %w", err)
		}
		remembered.Indexed = false
		remembered.IndexingStatus = "pending"
		remembered.RetryAt = &retryAt
	}

	link := models.WorkItemMemoryLink{
		WorkItemID: workItemID,
		MemoryType: "episode",
		MemoryID:   remembered.ID,
		Relation:   relation,
	}
	if err := tx.QueryRow(persistCtx,
		`INSERT INTO work_item_memory_links (work_item_id, memory_type, memory_id, relation)
		 VALUES ($1, 'episode', $2, $3)
		 RETURNING created_at`,
		workItemID, remembered.ID, relation,
	).Scan(&link.CreatedAt); err != nil {
		return nil, fmt.Errorf("link work memory: %w", err)
	}
	result := &RememberForWorkResult{RememberResult: remembered, Link: link}
	if err := insertWorkExecutionEvent(persistCtx, tx, namespaceID, workItemID, nil, "work.memory.remembered", workActionKeyDigest(actionKey), map[string]any{
		"episode_id":      remembered.ID,
		"relation":        relation,
		"indexing_status": remembered.IndexingStatus,
	}); err != nil {
		return nil, err
	}
	resultID := remembered.ID
	if err := storeWorkActionReceipt(persistCtx, tx, workItemID, nil, "remember", actionKey, requestHash, &resultID, result); err != nil {
		return nil, err
	}
	if err := tx.Commit(persistCtx); err != nil {
		return nil, fmt.Errorf("commit work memory: %w", err)
	}
	return result, nil
}
