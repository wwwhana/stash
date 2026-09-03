package brain

import (
	"context"
	"fmt"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/reasoner"
	"github.com/jackc/pgx/v5"
)

var ErrContradictionNotFound = fmt.Errorf("brain: contradiction not found")

// DetectContradictions checks a newly inserted fact against existing facts
// with the same (entity, property) in the same namespace.
// Returns the number of contradictions detected and auto-resolved.
func (b *Brain) DetectContradictions(ctx context.Context, nsID int64, fact *models.Fact) (detected, autoResolved int, err error) {
	if fact.Entity == nil || fact.Property == nil || *fact.Entity == "" || *fact.Property == "" {
		return 0, 0, nil
	}

	rows, err := b.pool.Query(ctx,
		`SELECT id, content, value, confidence FROM facts
		 WHERE namespace_id = $1 AND entity = $2 AND property = $3
		 AND id != $4 AND deleted_at IS NULL AND valid_until IS NULL`,
		nsID, *fact.Entity, *fact.Property, fact.ID,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("detect contradictions query: %w", err)
	}
	defer rows.Close()

	type existingFact struct {
		ID         int64
		Content    string
		Value      *string
		Confidence float32
	}

	var existing []existingFact
	for rows.Next() {
		var ef existingFact
		if err := rows.Scan(&ef.ID, &ef.Content, &ef.Value, &ef.Confidence); err != nil {
			return 0, 0, fmt.Errorf("scan existing fact: %w", err)
		}
		existing = append(existing, ef)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("existing facts rows: %w", err)
	}

	newValue := ""
	if fact.Value != nil {
		newValue = *fact.Value
	}

	for _, ef := range existing {
		oldValue := ""
		if ef.Value != nil {
			oldValue = *ef.Value
		}

		if oldValue == newValue {
			continue
		}

		cr, llmErr := b.reasoner.ReasonContradiction(ctx, *fact.Entity, *fact.Property, oldValue, newValue)
		if llmErr != nil {
			return detected, autoResolved, fmt.Errorf("classify facts %d and %d: %w", ef.ID, fact.ID, llmErr)
		}
		if cr == nil {
			return detected, autoResolved, fmt.Errorf("classify facts %d and %d: empty result", ef.ID, fact.ID)
		}
		if err := validateConfidence(cr.Confidence); err != nil {
			return detected, autoResolved, fmt.Errorf("classify facts %d and %d: %w", ef.ID, fact.ID, err)
		}

		switch cr.Classification {
		case reasoner.ClassificationReplacement:
			if cr.Confidence >= 0.9 {
				if err := b.autoSupersede(ctx, nsID, ef.ID, fact.ID, *fact.Entity, *fact.Property, oldValue, newValue, cr.Confidence); err != nil {
					return detected, autoResolved, err
				}
				detected++
				autoResolved++
			} else {
				if err := b.recordContradiction(ctx, nsID, ef.ID, fact.ID, *fact.Entity, *fact.Property, oldValue, newValue, cr.Confidence, "structured"); err != nil {
					return detected, autoResolved, err
				}
				detected++
			}

		case reasoner.ClassificationContradiction:
			if err := b.recordContradiction(ctx, nsID, ef.ID, fact.ID, *fact.Entity, *fact.Property, oldValue, newValue, cr.Confidence, "structured"); err != nil {
				return detected, autoResolved, err
			}
			detected++

		case reasoner.ClassificationCompatible:
		default:
			return detected, autoResolved, fmt.Errorf("classify facts %d and %d: unsupported classification %q", ef.ID, fact.ID, cr.Classification)
		}
	}

	return detected, autoResolved, nil
}

func (b *Brain) autoSupersede(ctx context.Context, nsID, oldFactID, newFactID int64, entity, property, oldValue, newValue string, confidence float32) error {
	now := time.Now().UTC()

	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin supersede transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		"UPDATE facts SET valid_until = $2, updated_at = $3 WHERE id = $1 AND deleted_at IS NULL AND valid_until IS NULL",
		oldFactID, now, now,
	)
	if err != nil {
		return fmt.Errorf("supersede old fact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFactNotFound
	}

	resolution := "superseded"
	updated, err := tx.Exec(ctx,
		`UPDATE contradictions
		 SET confidence = $4, method = 'auto', resolved = TRUE, resolution = $5, resolved_at = $6
		 WHERE namespace_id = $1 AND old_fact_id = $2 AND new_fact_id = $3 AND resolved = FALSE`,
		nsID, oldFactID, newFactID, confidence, resolution, now,
	)
	if err != nil {
		return fmt.Errorf("resolve existing auto-supersede contradiction: %w", err)
	}
	if updated.RowsAffected() == 0 {
		if _, err := tx.Exec(ctx,
			`INSERT INTO contradictions (namespace_id, old_fact_id, new_fact_id, entity, property, old_value, new_value, confidence, method, resolved, resolution, resolved_at)
			 SELECT $1, $2, $3, $4, $5, $6, $7, $8, 'auto', TRUE, $9, $10
			 WHERE NOT EXISTS (
			   SELECT 1 FROM contradictions
			   WHERE namespace_id = $1 AND old_fact_id = $2 AND new_fact_id = $3
			 )`,
			nsID, oldFactID, newFactID, entity, property, oldValue, newValue, confidence, resolution, now,
		); err != nil {
			return fmt.Errorf("insert auto-supersede contradiction: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit auto-supersede: %w", err)
	}

	return nil
}

func (b *Brain) recordContradiction(ctx context.Context, nsID, oldFactID, newFactID int64, entity, property, oldValue, newValue string, confidence float32, method string) error {
	_, err := b.pool.Exec(ctx,
		`INSERT INTO contradictions (namespace_id, old_fact_id, new_fact_id, entity, property, old_value, new_value, confidence, method)
		 SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9
		 WHERE NOT EXISTS (
		   SELECT 1 FROM contradictions
		   WHERE namespace_id = $1 AND old_fact_id = $2 AND new_fact_id = $3
		 )`,
		nsID, oldFactID, newFactID, entity, property, oldValue, newValue, confidence, method,
	)
	if err != nil {
		return fmt.Errorf("insert contradiction: %w", err)
	}
	return nil
}

// ListContradictions returns unresolved contradictions for namespaces matching the given paths.
func (b *Brain) ListContradictions(ctx context.Context, namespaceSlugs []string, page Pagination) ([]models.Contradiction, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, err
	}

	page = b.sanitizePage(page)

	rows, err := b.pool.Query(ctx,
		`SELECT id, namespace_id, old_fact_id, new_fact_id, entity, property, old_value, new_value,
		 confidence, method, resolved, resolution, resolved_at, created_at
		 FROM contradictions WHERE namespace_id = ANY($1) AND resolved = FALSE
		 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		nsIDs, page.Limit, page.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list contradictions: %w", err)
	}
	defer rows.Close()

	var result []models.Contradiction
	for rows.Next() {
		var c models.Contradiction
		if err := rows.Scan(
			&c.ID, &c.NamespaceID, &c.OldFactID, &c.NewFactID,
			&c.Entity, &c.Property, &c.OldValue, &c.NewValue,
			&c.Confidence, &c.Method, &c.Resolved, &c.Resolution,
			&c.ResolvedAt, &c.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan contradiction: %w", err)
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

// ResolveContradiction marks a contradiction as resolved with the given resolution.
func (b *Brain) ResolveContradiction(ctx context.Context, id int64, resolution string) error {
	now := time.Now().UTC()

	tag, err := b.pool.Exec(ctx,
		`UPDATE contradictions SET resolved = TRUE, resolution = $2, resolved_at = $3 WHERE id = $1 AND resolved = FALSE`,
		id, resolution, now,
	)
	if err != nil {
		return fmt.Errorf("resolve contradiction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrContradictionNotFound
	}
	return nil
}

// GetContradiction returns a single contradiction by ID.
func (b *Brain) GetContradiction(ctx context.Context, id int64) (*models.Contradiction, error) {
	var c models.Contradiction
	err := b.pool.QueryRow(ctx,
		`SELECT id, namespace_id, old_fact_id, new_fact_id, entity, property, old_value, new_value,
		 confidence, method, resolved, resolution, resolved_at, created_at
		 FROM contradictions WHERE id = $1`,
		id,
	).Scan(
		&c.ID, &c.NamespaceID, &c.OldFactID, &c.NewFactID,
		&c.Entity, &c.Property, &c.OldValue, &c.NewValue,
		&c.Confidence, &c.Method, &c.Resolved, &c.Resolution,
		&c.ResolvedAt, &c.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrContradictionNotFound
		}
		return nil, fmt.Errorf("get contradiction: %w", err)
	}
	return &c, nil
}
