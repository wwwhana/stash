package brain

import (
	"context"
	"fmt"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pgvector/pgvector-go"
)

// QueryFacts returns facts across namespaces matching the given slug paths, within an optional time range.
// Each path matches itself and all descendants.
func (b *Brain) QueryFacts(ctx context.Context, namespaceSlugs []string, since, until *time.Time, page Pagination) ([]models.Fact, error) {
	return b.QueryFactsFiltered(ctx, namespaceSlugs, since, until, "", page)
}

// QueryFactsFiltered is QueryFacts with an optional text search over the fact
// content and its structured entity/property/value fields.
func (b *Brain) QueryFactsFiltered(ctx context.Context, namespaceSlugs []string, since, until *time.Time, textQuery string, page Pagination) ([]models.Fact, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, err
	}

	page = b.sanitizePage(page)

	query := `SELECT id, namespace_id, content, embedding, COALESCE(embedding_model, ''), confidence,
	          entity, property, value, valid_from, valid_until, created_at, updated_at, deleted_at
	          FROM facts WHERE namespace_id = ANY($1) AND deleted_at IS NULL AND valid_until IS NULL`
	args := []any{nsIDs}
	argN := 1

	if since != nil {
		argN++
		query += fmt.Sprintf(" AND created_at >= $%d", argN)
		args = append(args, *since)
	}
	if until != nil {
		argN++
		query += fmt.Sprintf(" AND created_at <= $%d", argN)
		args = append(args, *until)
	}
	for _, token := range searchTextTokens(textQuery) {
		argN++
		pattern := "%" + escapeLikePattern(token) + "%"
		query += fmt.Sprintf(" AND (content ILIKE $%d ESCAPE '\\' OR COALESCE(entity, '') ILIKE $%d ESCAPE '\\' OR COALESCE(property, '') ILIKE $%d ESCAPE '\\' OR COALESCE(value, '') ILIKE $%d ESCAPE '\\')", argN, argN, argN, argN)
		args = append(args, pattern)
	}

	argN++
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", argN)
	args = append(args, page.Limit)

	argN++
	query += fmt.Sprintf(" OFFSET $%d", argN)
	args = append(args, page.Offset)

	rows, err := b.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query facts: %w", err)
	}
	defer rows.Close()

	var facts []models.Fact
	for rows.Next() {
		var f models.Fact
		var embedding *pgvector.Vector
		if err := rows.Scan(
			&f.ID, &f.NamespaceID, &f.Content, &embedding, &f.EmbeddingModel,
			&f.Confidence, &f.Entity, &f.Property, &f.Value,
			&f.ValidFrom, &f.ValidUntil,
			&f.CreatedAt, &f.UpdatedAt, &f.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan fact: %w", err)
		}
		if embedding != nil {
			f.Embedding = *embedding
		}
		facts = append(facts, f)
	}
	return facts, rows.Err()
}

// UpdateFactConfidence updates the confidence score of a fact.
func (b *Brain) UpdateFactConfidence(ctx context.Context, factID int64, confidence float32) error {
	if err := validateConfidence(confidence); err != nil {
		return err
	}
	tag, err := b.pool.Exec(ctx,
		"UPDATE facts SET confidence = $2, updated_at = now() WHERE id = $1 AND deleted_at IS NULL AND valid_until IS NULL",
		factID, confidence,
	)
	if err != nil {
		return fmt.Errorf("update fact confidence: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFactNotFound
	}
	return nil
}

// PurgeFact removes a fact by ID.
//
// Soft by default (deleted_at), matching PurgeEpisode. Pass hard=true for an
// irreversible DELETE. A soft-deleted fact stops appearing in recall and
// query_facts but its provenance rows in fact_sources remain intact, so the
// derivation can still be audited after the fact is retired.
func (b *Brain) PurgeFact(ctx context.Context, factID int64, hard bool) error {
	var (
		tag pgconn.CommandTag
		err error
	)
	if hard {
		tag, err = b.pool.Exec(ctx, "DELETE FROM facts WHERE id = $1", factID)
	} else {
		tag, err = b.pool.Exec(ctx,
			"UPDATE facts SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL",
			factID,
		)
	}
	if err != nil {
		return fmt.Errorf("purge fact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFactNotFound
	}
	return nil
}

// RestoreFact clears deleted_at, undoing a soft delete.
func (b *Brain) RestoreFact(ctx context.Context, factID int64) error {
	tag, err := b.pool.Exec(ctx,
		"UPDATE facts SET deleted_at = NULL WHERE id = $1 AND deleted_at IS NOT NULL",
		factID,
	)
	if err != nil {
		return fmt.Errorf("restore fact: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFactNotFound
	}
	return nil
}

// GetFact returns a single fact by ID.
func (b *Brain) GetFact(ctx context.Context, factID int64) (*models.Fact, error) {
	var f models.Fact
	var embedding *pgvector.Vector
	err := b.pool.QueryRow(ctx,
		`SELECT id, namespace_id, content, embedding, COALESCE(embedding_model, ''), confidence,
			 entity, property, value, valid_from, valid_until, created_at, updated_at, deleted_at
			 FROM facts WHERE id = $1 AND deleted_at IS NULL AND valid_until IS NULL`,
		factID,
	).Scan(
		&f.ID, &f.NamespaceID, &f.Content, &embedding, &f.EmbeddingModel,
		&f.Confidence, &f.Entity, &f.Property, &f.Value,
		&f.ValidFrom, &f.ValidUntil,
		&f.CreatedAt, &f.UpdatedAt, &f.DeletedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrFactNotFound
		}
		return nil, fmt.Errorf("get fact: %w", err)
	}
	if embedding != nil {
		f.Embedding = *embedding
	}
	return &f, nil
}

// QueryRelationships returns relationships across namespaces matching the given slug paths.
// Each path matches itself and all descendants.
func (b *Brain) QueryRelationships(ctx context.Context, namespaceSlugs []string, page Pagination) ([]models.Relationship, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, err
	}

	page = b.sanitizePage(page)

	rows, err := b.pool.Query(ctx,
		`SELECT r.id, r.namespace_id, r.from_entity, r.relation_type, r.to_entity, r.confidence, r.source_fact_id, r.created_at, r.deleted_at
		 FROM relationships r
		 JOIN facts f ON f.id = r.source_fact_id AND f.namespace_id = r.namespace_id AND f.deleted_at IS NULL AND f.valid_until IS NULL
		 WHERE r.namespace_id = ANY($1) AND r.deleted_at IS NULL ORDER BY r.id LIMIT $2 OFFSET $3`,
		nsIDs, page.Limit, page.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("query relationships: %w", err)
	}
	defer rows.Close()

	var rels []models.Relationship
	for rows.Next() {
		var r models.Relationship
		if err := rows.Scan(&r.ID, &r.NamespaceID, &r.FromEntity, &r.RelationType, &r.ToEntity, &r.Confidence, &r.SourceFactID, &r.CreatedAt, &r.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan relationship: %w", err)
		}
		rels = append(rels, r)
	}
	return rels, rows.Err()
}

// QueryPatterns returns patterns across namespaces matching the given slug paths.
// Each path matches itself and all descendants.
func (b *Brain) QueryPatterns(ctx context.Context, namespaceSlugs []string, page Pagination) ([]models.Pattern, error) {
	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return nil, err
	}

	page = b.sanitizePage(page)

	rows, err := b.pool.Query(ctx,
		`SELECT id, namespace_id, content, confidence, source_fact_ids, source_rel_ids, coherence_score, created_at, updated_at, deleted_at
		 FROM patterns WHERE namespace_id = ANY($1) AND deleted_at IS NULL ORDER BY id LIMIT $2 OFFSET $3`,
		nsIDs, page.Limit, page.Offset,
	)
	if err != nil {
		return nil, fmt.Errorf("query patterns: %w", err)
	}
	defer rows.Close()

	var patterns []models.Pattern
	for rows.Next() {
		var p models.Pattern
		if err := rows.Scan(&p.ID, &p.NamespaceID, &p.Content, &p.Confidence, &p.SourceFactIDs, &p.SourceRelIDs, &p.CoherenceScore, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt); err != nil {
			return nil, fmt.Errorf("scan pattern: %w", err)
		}
		patterns = append(patterns, p)
	}
	return patterns, rows.Err()
}
