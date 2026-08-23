package brain

import (
	"context"
	"fmt"

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
// Only the `embedding` column is rewritten; `content` is never touched, so this is
// non-destructive and safe to re-run. Rows that fail to embed are counted and
// skipped rather than aborting the pass, so one bad row cannot strand the rest.
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
				continue
			}
			if _, err := b.pool.Exec(ctx,
				fmt.Sprintf("UPDATE %s SET embedding = $1 WHERE id = $2", table),
				pgvector.NewVector(vec), r.id,
			); err != nil {
				res.Failed++
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
