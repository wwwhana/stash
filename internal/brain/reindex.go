package brain

import (
	"context"
	"fmt"
	"time"

	"github.com/pgvector/pgvector-go"
)

// ReindexResult reports what a Reindex pass did.
type ReindexResult struct {
	EpisodesTotal int `json:"episodes_total"`
	EpisodesDone  int `json:"episodes_done"`
	FactsTotal    int `json:"facts_total"`
	FactsDone     int `json:"facts_done"`
	Failed        int `json:"failed"`
}

// Reindex recomputes the embedding for every stored episode and fact.
//
// Needed when the embedding *input* changes while the model stays the same — the
// e5 "passage: " prefix is the motivating case. Vectors written before such a change
// occupy a different region of the space than newly embedded queries, so recall
// quietly degrades until everything is re-embedded.
//
// Only embedding state is rewritten; `content` is never touched, so this is
// non-destructive and safe to re-run. Rows that fail to embed remain queued for
// the background retry worker instead of leaving an old-model vector in place.
func (b *Brain) Reindex(ctx context.Context, dryRun bool, progress func(table string, done, total int)) (ReindexResult, error) {
	var res ReindexResult

	for _, table := range []string{"episodes", "facts"} {
		var total int
		if err := b.pool.QueryRow(ctx,
			fmt.Sprintf("SELECT count(*) FROM %s WHERE deleted_at IS NULL", table),
		).Scan(&total); err != nil {
			return res, fmt.Errorf("count %s: %w", table, err)
		}

		if table == "episodes" {
			res.EpisodesTotal = total
		} else {
			res.FactsTotal = total
		}

		if dryRun {
			continue
		}

		// Clear the old vector before starting. If a provider request fails,
		// recall must not silently use a vector from the previous model.
		reason := fmt.Sprintf("manual reindex requested for model %q; waiting for a fresh vector", b.embedder.Model())
		if _, err := b.pool.Exec(ctx, fmt.Sprintf(`
			UPDATE %s
			SET embedding = NULL,
			    embedding_model = $1,
			    embedding_attempts = 0,
			    embedding_last_error = $2,
			    embedding_retry_at = now(),
			    embedding_updated_at = now()
			WHERE deleted_at IS NULL
		`, table), b.embedder.Model(), reason); err != nil {
			return res, fmt.Errorf("queue %s reindex: %w", table, err)
		}

		type row struct {
			id      int64
			content string
		}

		rows, err := b.pool.Query(ctx,
			fmt.Sprintf("SELECT id, content FROM %s WHERE deleted_at IS NULL ORDER BY id", table),
		)
		if err != nil {
			return res, fmt.Errorf("select %s: %w", table, err)
		}

		var batch []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.content); err != nil {
				rows.Close()
				return res, fmt.Errorf("scan %s: %w", table, err)
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return res, fmt.Errorf("%s rows: %w", table, err)
		}

		done := 0
		for _, r := range batch {
			vec, err := b.embedder.Embed(ctx, r.content)
			if err != nil {
				res.Failed++
				next := time.Now().UTC().Add(embeddingRetryDelay(
					b.config.EmbeddingRetryInterval,
					b.config.EmbeddingRetryMaxInterval,
					1,
				))
				_ = b.recordEmbeddingFailure(ctx, table, r.id, err, next)
				continue
			}
			if _, err := b.pool.Exec(ctx,
				fmt.Sprintf(`UPDATE %s
					SET embedding = $1,
					    embedding_model = $2,
					    embedding_last_error = NULL,
					    embedding_retry_at = NULL,
					    embedding_updated_at = now()
					WHERE id = $3`, table),
				pgvector.NewVector(vec), b.embedder.Model(), r.id,
			); err != nil {
				res.Failed++
				next := time.Now().UTC().Add(embeddingRetryDelay(
					b.config.EmbeddingRetryInterval,
					b.config.EmbeddingRetryMaxInterval,
					1,
				))
				_ = b.recordEmbeddingFailure(ctx, table, r.id, err, next)
				continue
			}
			done++
			if progress != nil && done%25 == 0 {
				progress(table, done, len(batch))
			}
		}

		if table == "episodes" {
			res.EpisodesDone = done
		} else {
			res.FactsDone = done
		}
	}

	return res, nil
}
