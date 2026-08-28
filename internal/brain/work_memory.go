package brain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/pgvector/pgvector-go"
)

// RememberForWorkResult returns both the durable episode status and its typed
// link to the originating work item.
type RememberForWorkResult struct {
	RememberResult
	Link models.WorkItemMemoryLink `json:"link"`
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
