package brain

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/pgvector/pgvector-go"
)

// RecallResult is a unified result from semantic search across episodes and facts.
type RecallResult struct {
	ID          int64   `json:"id"`
	NamespaceID int64   `json:"namespace_id"`
	Content     string  `json:"content"`
	Confidence  float32 `json:"confidence,omitempty"`
	Score       float32 `json:"score"`
	Type        string  `json:"type"`
	OccurredAt  string  `json:"occurred_at,omitempty"`
	ValidFrom   string  `json:"valid_from,omitempty"`
	CreatedAt   string  `json:"created_at"`

	// SourceEpisodeIDs lists the episodes a fact was synthesized from (facts only).
	//
	// Facts are written by an LLM during consolidation, so they can be wrong or
	// overstated in ways the wording does not reveal. Returning the provenance lets a
	// caller pull the original episodes and check the claim instead of trusting it.
	// The fact_sources table has always recorded this; it was simply never read.
	SourceEpisodeIDs []int64 `json:"source_episode_ids,omitempty"`

	// VectorScore and KeywordScore keep the raw per-engine similarities.
	//
	// Score itself is an RRF value derived from rank position only, so it says
	// nothing about how related a result actually is: rank 1 always scores 1/61
	// whether the match was near-exact or nonsense. Without these fields a caller
	// cannot tell "this is my memory" from "this was merely the closest row".
	VectorScore  float32 `json:"vector_score,omitempty"`
	KeywordScore float32 `json:"keyword_score,omitempty"`
}

// Recall searches episodes and facts across the given namespaces, fusing vector and
// keyword results. Each namespace path matches itself and all descendants.
//
// Retained for backward compatibility — see RecallWithOptions for the similarity floor.
func (b *Brain) Recall(ctx context.Context, namespaces []string, query string, limit int) ([]RecallResult, error) {
	return b.RecallWithOptions(ctx, namespaces, query, limit, RecallOptions{})
}

// RecallOptions tunes retrieval. The zero value reproduces the previous behaviour.
type RecallOptions struct {
	// MinScore drops vector candidates whose cosine similarity falls below this
	// value (0..1). Zero disables the floor.
	//
	// Why it matters: the vector query is `ORDER BY distance LIMIT n`, which always
	// returns the n closest rows no matter how far away they are. With only recipes
	// stored, a query about Kubernetes still returns recipes — and RRF then hides
	// this, because it scores purely by rank: position 1 is 1/61 whether the match
	// was near-exact or nonsense.
	MinScore float32
}

// RecallWithOptions is Recall with retrieval controls.
func (b *Brain) RecallWithOptions(ctx context.Context, namespaces []string, query string, limit int, opts RecallOptions) ([]RecallResult, error) {
	if err := validateContent(query); err != nil {
		return nil, err
	}
	if err := validateScore(opts.MinScore); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	if b.config.MaxResultSize > 0 && limit > b.config.MaxResultSize {
		limit = b.config.MaxResultSize
	}

	vec, err := b.embedder.EmbedQuery(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}

	pgVec := pgvector.NewVector(vec)

	nsIDs, err := b.resolveNamespaceIDs(ctx, namespaces)
	if err != nil {
		return nil, err
	}

	// Collect facts and episodes as INDEPENDENT candidate pools, each up to `limit`.
	//
	// Previously episodes only got the slots facts left over (`limit - len(results)`),
	// so once facts filled the limit the episode query was skipped entirely — a
	// perfectly matching episode could never surface no matter how high its score.
	// Both pools are now gathered in full and merged by score below.
	factLimit := limit
	factSQL, factArgs, err := b.queries.RecallFacts(nsIDs, pgVec, factLimit, opts.MinScore)
	if err != nil {
		return nil, fmt.Errorf("build fact query: %w", err)
	}

	factRows, err := b.pool.Query(ctx, factSQL, factArgs...)
	if err != nil {
		return nil, fmt.Errorf("query facts: %w", err)
	}
	defer factRows.Close()

	var results []RecallResult
	for factRows.Next() {
		var id int64
		var namespaceID int64
		var content string
		var confidence float32
		var score float32
		var createdAt time.Time

		if err := factRows.Scan(&id, &namespaceID, &content, &confidence, &createdAt, &score); err != nil {
			return nil, fmt.Errorf("scan fact: %w", err)
		}
		results = append(results, RecallResult{
			ID:          id,
			NamespaceID: namespaceID,
			Content:     content,
			Confidence:  confidence,
			Score:       score,
			Type:        "fact",
			CreatedAt:   createdAt.Format(time.RFC3339),
		})
	}
	if err := factRows.Err(); err != nil {
		return nil, fmt.Errorf("fact rows: %w", err)
	}

	// Episodes get their own full budget, not the leftovers.
	episodeLimit := limit
	if episodeLimit > 0 {
		epSQL, epArgs, err := b.queries.RecallEpisodes(nsIDs, pgVec, episodeLimit, opts.MinScore)
		if err != nil {
			return nil, fmt.Errorf("build episode query: %w", err)
		}

		epRows, err := b.pool.Query(ctx, epSQL, epArgs...)
		if err != nil {
			return nil, fmt.Errorf("query episodes: %w", err)
		}
		defer epRows.Close()

		for epRows.Next() {
			var id int64
			var namespaceID int64
			var content string
			var score float32
			var occurredAt time.Time
			var createdAt time.Time

			if err := epRows.Scan(&id, &namespaceID, &content, &occurredAt, &createdAt, &score); err != nil {
				return nil, fmt.Errorf("scan episode: %w", err)
			}
			results = append(results, RecallResult{
				ID:          id,
				NamespaceID: namespaceID,
				Content:     content,
				Score:       score,
				Type:        "episode",
				OccurredAt:  occurredAt.Format(time.RFC3339),
				CreatedAt:   createdAt.Format(time.RFC3339),
			})
		}
		if err := epRows.Err(); err != nil {
			return nil, fmt.Errorf("episode rows: %w", err)
		}
	}

	// Keyword (trigram) pass, fused with the vector results below.
	// Vectors match meaning; they do not reliably match literal tokens such as
	// ticket keys ("FROMM-4414") or symbol names. Trigram search covers that gap
	// and is language-neutral, which matters because the corpus is mixed Korean/English.
	keywordResults := b.keywordCandidates(ctx, nsIDs, query, limit)

	merged := fuseRRF(results, keywordResults, limit)
	b.attachFactSources(ctx, merged)
	return merged, nil
}

// attachFactSources fills SourceEpisodeIDs for the fact rows in the result set.
//
// Done after fusion so only the rows actually being returned are looked up, in a
// single query rather than one per fact. Provenance is a convenience: if the lookup
// fails, results are returned without it rather than failing the whole recall.
func (b *Brain) attachFactSources(ctx context.Context, results []RecallResult) {
	var factIDs []int64
	for _, r := range results {
		if r.Type == "fact" {
			factIDs = append(factIDs, r.ID)
		}
	}
	if len(factIDs) == 0 {
		return
	}

	rows, err := b.pool.Query(ctx,
		`SELECT fact_id, episode_id FROM fact_sources WHERE fact_id = ANY($1) ORDER BY fact_id, episode_id`,
		factIDs,
	)
	if err != nil {
		return
	}
	defer rows.Close()

	sources := make(map[int64][]int64, len(factIDs))
	for rows.Next() {
		var factID, episodeID int64
		if err := rows.Scan(&factID, &episodeID); err != nil {
			return
		}
		sources[factID] = append(sources[factID], episodeID)
	}

	for i := range results {
		if results[i].Type == "fact" {
			results[i].SourceEpisodeIDs = sources[results[i].ID]
		}
	}
}

// keywordCandidates runs the trigram search over both pools.
// Keyword search is a supplement: if it fails (e.g. pg_trgm missing), recall still
// returns vector results rather than erroring out.
func (b *Brain) keywordCandidates(ctx context.Context, nsIDs []int64, query string, limit int) []RecallResult {
	var out []RecallResult

	if sql, args, err := b.queries.KeywordFacts(nsIDs, query, limit); err == nil {
		if rows, err := b.pool.Query(ctx, sql, args...); err == nil {
			for rows.Next() {
				var r RecallResult
				var createdAt time.Time
				if err := rows.Scan(&r.ID, &r.NamespaceID, &r.Content, &r.Confidence, &createdAt, &r.Score); err != nil {
					break
				}
				r.Type = "fact"
				r.CreatedAt = createdAt.Format(time.RFC3339)
				out = append(out, r)
			}
			rows.Close()
		}
	}

	if sql, args, err := b.queries.KeywordEpisodes(nsIDs, query, limit); err == nil {
		if rows, err := b.pool.Query(ctx, sql, args...); err == nil {
			for rows.Next() {
				var r RecallResult
				var occurredAt, createdAt time.Time
				if err := rows.Scan(&r.ID, &r.NamespaceID, &r.Content, &occurredAt, &createdAt, &r.Score); err != nil {
					break
				}
				r.Type = "episode"
				r.OccurredAt = occurredAt.Format(time.RFC3339)
				r.CreatedAt = createdAt.Format(time.RFC3339)
				out = append(out, r)
			}
			rows.Close()
		}
	}

	return out
}

// fuseRRF merges two ranked lists with Reciprocal Rank Fusion.
//
// RRF is used instead of blending the raw scores because cosine similarity and
// trigram similarity are not on a comparable scale — averaging them would let one
// metric's range dominate. RRF only looks at rank position, so both signals count
// equally, and an item ranked well by both rises above an item that only one liked.
func fuseRRF(vectorResults, keywordResults []RecallResult, limit int) []RecallResult {
	const k = 60 // standard RRF damping constant

	type key struct {
		typ string
		id  int64
	}

	fused := make(map[key]float64)
	item := make(map[key]RecallResult)
	vecScore := make(map[key]float32)
	kwScore := make(map[key]float32)

	add := func(list []RecallResult, isVector bool) {
		// Rank within each list must be by descending score.
		sorted := make([]RecallResult, len(list))
		copy(sorted, list)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Score > sorted[j].Score })

		for rank, r := range sorted {
			kk := key{typ: r.Type, id: r.ID}
			fused[kk] += 1.0 / float64(k+rank+1)
			// 엔진별 원점수를 따로 보관한다. RRF 는 순위만 남기고 유사도를 지우므로,
			// 이걸 잃으면 "정확한 일치"와 "그냥 가장 가까웠던 행"을 구분할 수 없다.
			if isVector {
				vecScore[kk] = r.Score
			} else {
				kwScore[kk] = r.Score
			}
			if _, seen := item[kk]; !seen {
				item[kk] = r
			}
		}
	}

	add(vectorResults, true)
	add(keywordResults, false)

	merged := make([]RecallResult, 0, len(item))
	for kk, r := range item {
		r.Score = float32(fused[kk])
		r.VectorScore = vecScore[kk]
		r.KeywordScore = kwScore[kk]
		merged = append(merged, r)
	}

	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].ID < merged[j].ID // stable output for equal scores
	})

	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}
