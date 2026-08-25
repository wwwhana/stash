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

// Remember stores a new episode in the given namespace.
// If occurredAt is nil, the current time is used.
// Returns the episode ID on success.
func (b *Brain) Remember(ctx context.Context, namespaceSlug, content string, occurredAt *time.Time) (int64, error) {
	if err := validateContent(content); err != nil {
		return 0, err
	}
	if err := validatePath(namespaceSlug); err != nil {
		return 0, err
	}

	nsID, err := b.resolveNamespaceID(ctx, namespaceSlug)
	if err != nil {
		return 0, err
	}

	occurred := time.Now().UTC()
	if occurredAt != nil {
		occurred = *occurredAt
	}

	vec, err := b.embedder.Embed(ctx, content)
	if err != nil {
		return 0, fmt.Errorf("embed: %w", err)
	}

	var id int64
	err = b.pool.QueryRow(ctx,
		`INSERT INTO episodes (namespace_id, content, embedding, embedding_model, occurred_at)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		nsID, content, pgvector.NewVector(vec), b.embedder.Model(), occurred,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert episode: %w", err)
	}
	return id, nil
}

// ForgetOptions tunes how ForgetEpisodeMatch selects and removes a match.
//
// The zero value reproduces the original behaviour exactly: delete the single
// nearest episode, whatever its similarity. Options are opt-in so existing
// callers are unaffected.
type ForgetOptions struct {
	// MinScore refuses to delete unless cosine similarity reaches this value (0..1).
	// Zero disables the check.
	//
	// Without it, forget always deletes *something*: once the intended targets are
	// gone, the next call silently removes whatever happens to be closest. A caller
	// looping over a set of deletions has no way to notice it has overshot.
	MinScore float32

	// DryRun reports the match that would be deleted without deleting it.
	DryRun bool
}

// ForgetResult describes what a forget call matched and whether it removed it.
//
// The original API returned only an error, so a caller could not tell which
// episode it had just destroyed. Returning the match makes overshoot auditable.
type ForgetResult struct {
	ID      int64   `json:"id"`
	Content string  `json:"content"`
	Score   float32 `json:"score"`
	Deleted bool    `json:"deleted"`
	// Skipped is set when a match existed but fell below MinScore.
	Skipped bool `json:"skipped,omitempty"`
}

// ForgetEpisode soft-deletes the episode that best matches the query across the
// given namespaces. Retained for backward compatibility — see ForgetEpisodeMatch
// for threshold and dry-run control.
func (b *Brain) ForgetEpisode(ctx context.Context, namespaceSlugs []string, query string) error {
	_, err := b.ForgetEpisodeMatch(ctx, namespaceSlugs, query, ForgetOptions{})
	return err
}

// ForgetEpisodeMatch finds the nearest episode to the query and, unless DryRun is
// set or the match falls below MinScore, soft-deletes it. The matched episode is
// always returned so the caller can verify what was affected.
func (b *Brain) ForgetEpisodeMatch(
	ctx context.Context,
	namespaceSlugs []string,
	query string,
	opts ForgetOptions,
) (ForgetResult, error) {
	var res ForgetResult

	if err := validateContent(query); err != nil {
		return res, err
	}
	if err := validateScore(opts.MinScore); err != nil {
		return res, err
	}
	for _, slug := range namespaceSlugs {
		if err := validatePath(slug); err != nil {
			return res, err
		}
	}

	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaceSlugs)
	if err != nil {
		return res, err
	}

	vec, err := b.embedder.EmbedQuery(ctx, query)
	if err != nil {
		return res, fmt.Errorf("embed: %w", err)
	}
	pgVec := pgvector.NewVector(vec)

	const selectOne = `SELECT id, content, 1 - (embedding <=> $2) AS score
	                   FROM episodes
	                   WHERE %s AND deleted_at IS NULL AND embedding IS NOT NULL
	                   ORDER BY embedding <=> $2 LIMIT 1`

	if len(nsIDs) == 1 {
		err = b.pool.QueryRow(ctx,
			fmt.Sprintf(selectOne, "namespace_id = $1"), nsIDs[0], pgVec,
		).Scan(&res.ID, &res.Content, &res.Score)
	} else {
		err = b.pool.QueryRow(ctx,
			fmt.Sprintf(selectOne, "namespace_id = ANY($1)"), nsIDs, pgVec,
		).Scan(&res.ID, &res.Content, &res.Score)
	}
	if err != nil {
		if err == pgx.ErrNoRows {
			return res, ErrEpisodeNotFound
		}
		return res, fmt.Errorf("find episode to forget: %w", err)
	}

	if opts.MinScore > 0 && res.Score < opts.MinScore {
		res.Skipped = true
		return res, nil
	}

	if opts.DryRun {
		return res, nil
	}

	tag, err := b.pool.Exec(ctx,
		"UPDATE episodes SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL", res.ID,
	)
	if err != nil {
		return res, fmt.Errorf("soft delete episode: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return res, ErrEpisodeNotFound
	}

	res.Deleted = true
	return res, nil
}

// PurgeEpisode removes an episode by ID.
//
// Soft by default: the row is marked with deleted_at and stops appearing in
// recall, but the content survives and can be restored. Pass hard=true to issue
// a real DELETE, which is irreversible.
//
// Defaulting to soft matters because this store holds memory that exists nowhere
// else — a mistaken id, or a cleanup loop that runs one iteration too long,
// should be recoverable rather than final.
func (b *Brain) PurgeEpisode(ctx context.Context, episodeID int64, hard bool) error {
	var (
		tag pgconn.CommandTag
		err error
	)
	if hard {
		tag, err = b.pool.Exec(ctx, "DELETE FROM episodes WHERE id = $1", episodeID)
	} else {
		tag, err = b.pool.Exec(ctx,
			"UPDATE episodes SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL",
			episodeID,
		)
	}
	if err != nil {
		return fmt.Errorf("purge episode: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEpisodeNotFound
	}
	return nil
}

// RestoreEpisode clears deleted_at, undoing a soft delete.
func (b *Brain) RestoreEpisode(ctx context.Context, episodeID int64) error {
	tag, err := b.pool.Exec(ctx,
		"UPDATE episodes SET deleted_at = NULL WHERE id = $1 AND deleted_at IS NOT NULL",
		episodeID,
	)
	if err != nil {
		return fmt.Errorf("restore episode: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrEpisodeNotFound
	}
	return nil
}

// GetEpisode returns a single episode by ID.
func (b *Brain) GetEpisode(ctx context.Context, episodeID int64) (*models.Episode, error) {
	var e models.Episode
	err := b.pool.QueryRow(ctx,
		`SELECT id, namespace_id, content, embedding, embedding_model, occurred_at, created_at, deleted_at
		 FROM episodes WHERE id = $1 AND deleted_at IS NULL`,
		episodeID,
	).Scan(&e.ID, &e.NamespaceID, &e.Content, &e.Embedding, &e.EmbeddingModel, &e.OccurredAt, &e.CreatedAt, &e.DeletedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrEpisodeNotFound
		}
		return nil, fmt.Errorf("get episode: %w", err)
	}
	return &e, nil
}
