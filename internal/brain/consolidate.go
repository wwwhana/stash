package brain

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/observability"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// ConsolidationResult describes the outcome of a consolidation run.
type ConsolidationResult struct {
	Namespace                  string        `json:"namespace"`
	Duration                   time.Duration `json:"duration"`
	EpisodesRead               int           `json:"episodes_read"`
	FactsCreated               int           `json:"facts_created"`
	FactsDeduplicated          int           `json:"facts_deduplicated"`
	RelationshipsFound         int           `json:"relationships_found"`
	CausalLinksFound           int           `json:"causal_links_found"`
	PatternsFound              int           `json:"patterns_found"`
	ContradictionsFound        int           `json:"contradictions_found"`
	ContradictionsAutoResolved int           `json:"contradictions_auto_resolved"`
	GoalsAnnotated             int           `json:"goals_annotated"`
	GoalsSuggestedComplete     int           `json:"goals_suggested_complete"`
	FailureRepeatsDetected     int           `json:"failure_repeats_detected"`
	FailurePatternsFound       int           `json:"failure_patterns_found"`
	HypothesesAutoConfirmed    int           `json:"hypotheses_auto_confirmed"`
	HypothesesAutoRejected     int           `json:"hypotheses_auto_rejected"`
	HypothesesUpdated          int           `json:"hypotheses_updated"`
	FactsDecayed               int           `json:"facts_decayed"`
	FactsExpired               int           `json:"facts_expired"`
	LLMCalls                   int           `json:"llm_calls"`
	Errors                     []string      `json:"errors,omitempty"`
}

// Consolidate runs the full 8-stage consolidation pipeline for a namespace.
func (b *Brain) Consolidate(ctx context.Context, namespaceSlug string) (ConsolidationResult, error) {
	if err := validatePath(namespaceSlug); err != nil {
		return ConsolidationResult{}, err
	}
	nsID, err := b.resolveNamespaceID(ctx, namespaceSlug)
	if err != nil {
		return ConsolidationResult{}, err
	}
	return b.ConsolidateByID(ctx, nsID)
}

// ConsolidateByID runs the full 8-stage consolidation pipeline for a namespace by ID.
// Stages run in execution order:
// 1. Episodes -> Facts                    (cluster + synthesize, with inline contradiction detection)
// 2. Facts -> Relationships               (extract entity edges)
// 3. Facts -> Causal Links                (cause/effect relationships)
// 4. Goal Progress Inference              (annotate goals from new facts)
// 5. Failure Pattern Detection            (recurring failures across episodes)
// 6. Facts + Relationships -> Patterns    (extract abstractions)
// 7. Hypothesis Evidence Scanning         (auto-confirm/reject pending hypotheses)
// 8. Confidence Decay                     (pure-SQL, no LLM)
func (b *Brain) ConsolidateByID(ctx context.Context, nsID int64) (ConsolidationResult, error) {
	start := time.Now()
	b.consolidationMu.Lock()
	defer b.consolidationMu.Unlock()
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
	}

	var namespaceSlug string
	if err := b.pool.QueryRow(ctx, "SELECT slug FROM namespaces WHERE id = $1", nsID).Scan(&namespaceSlug); err != nil {
		if err == pgx.ErrNoRows {
			return ConsolidationResult{}, ErrNamespaceNotFound
		}
		return ConsolidationResult{}, fmt.Errorf("resolve consolidation namespace: %w", err)
	}

	result := ConsolidationResult{Namespace: namespaceSlug}

	cp, err := b.GetOrCreateConsolidationProgress(ctx, nsID)
	if err != nil {
		return result, fmt.Errorf("get progress: %w", err)
	}

	// Stage 1: Episodes -> Facts (+ Stage 4: Contradiction detection)
	if ctx.Err() == nil {
		factsCreated, factsDeduped, episodesRead, llmCalls, contFound, contAuto, errs := b.consolidateEpisodesToFacts(ctx, nsID, cp)
		result.FactsCreated = factsCreated
		result.FactsDeduplicated = factsDeduped
		result.EpisodesRead = episodesRead
		result.LLMCalls += llmCalls
		result.ContradictionsFound = contFound
		result.ContradictionsAutoResolved = contAuto
		result.Errors = append(result.Errors, errs...)
	}

	// Stage 2: Facts -> Relationships
	if ctx.Err() == nil {
		relCount, llmCalls, errs := b.consolidateFactsToRelationships(ctx, nsID, cp)
		result.RelationshipsFound = relCount
		result.LLMCalls += llmCalls
		result.Errors = append(result.Errors, errs...)
	}

	// Stage 3.5: Facts -> Causal Links
	if ctx.Err() == nil {
		causalCount, llmCalls, errs := b.consolidateFactsToCausalLinks(ctx, nsID, cp)
		result.CausalLinksFound = causalCount
		result.LLMCalls += llmCalls
		result.Errors = append(result.Errors, errs...)
	}

	// Stage 6: Goal Progress Inference
	if ctx.Err() == nil {
		annotated, suggestedComplete, llmCalls, errs := b.consolidateGoalProgress(ctx, nsID, cp)
		result.GoalsAnnotated = annotated
		result.GoalsSuggestedComplete = suggestedComplete
		result.LLMCalls += llmCalls
		result.Errors = append(result.Errors, errs...)
	}

	// Stage 7: Failure Pattern Detection
	if ctx.Err() == nil {
		repeats, patterns, llmCalls, errs := b.consolidateFailurePatterns(ctx, nsID, cp)
		result.FailureRepeatsDetected = repeats
		result.FailurePatternsFound = patterns
		result.LLMCalls += llmCalls
		result.Errors = append(result.Errors, errs...)
	}

	// Stage 3: Facts + Relationships -> Patterns
	if ctx.Err() == nil {
		patCount, llmCalls, errs := b.consolidateToPatterns(ctx, nsID, cp)
		result.PatternsFound = patCount
		result.LLMCalls += llmCalls
		result.Errors = append(result.Errors, errs...)
	}

	// Stage 8: Hypothesis Evidence Scanning
	if ctx.Err() == nil {
		autoConfirmed, autoRejected, updated, llmCalls, errs := b.consolidateHypothesisEvidence(ctx, nsID, cp)
		result.HypothesesAutoConfirmed = autoConfirmed
		result.HypothesesAutoRejected = autoRejected
		result.HypothesesUpdated = updated
		result.LLMCalls += llmCalls
		result.Errors = append(result.Errors, errs...)
	}

	// Stage 5: Confidence decay
	if ctx.Err() == nil {
		decayResult, err := b.DecayConfidence(ctx, nsID)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("decay confidence: %v", err))
		} else {
			result.FactsDecayed = decayResult.FactsDecayed
			result.FactsExpired = decayResult.FactsExpired
		}
	}

	// Save progress
	now := time.Now().UTC()
	cp.LastRun = &now
	saveCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		saveCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}
	if err := b.SaveConsolidationProgress(saveCtx, *cp); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("save progress: %v", err))
	}

	result.Duration = time.Since(start)
	observability.RecordConsolidation(observability.Observation{
		Namespace:          namespaceSlug,
		EventsRead:         result.EpisodesRead,
		EventsProcessed:    result.EpisodesRead,
		FactsCreated:       result.FactsCreated,
		FactsDeduplicated:  result.FactsDeduplicated,
		RelationshipsFound: result.RelationshipsFound,
		LLMCalls:           result.LLMCalls,
		Duration:           result.Duration,
		Errors:             len(result.Errors),
	})

	return result, nil
}

// --- Stage 1: Episodes -> Facts ---

func (b *Brain) consolidateEpisodesToFacts(ctx context.Context, nsID int64, cp *models.ConsolidationProgress) (created, deduped, read, llmCalls, contradictionsFound, contradictionsAutoResolved int, errs []string) {
	sql, args, err := b.queries.FetchEpisodes(nsID, cp.LastEpisodeID, b.config.BatchSize)
	if err != nil {
		errs = append(errs, fmt.Sprintf("build fetch episodes: %v", err))
		return
	}

	rows, err := b.pool.Query(ctx, sql, args...)
	if err != nil {
		errs = append(errs, fmt.Sprintf("fetch episodes: %v", err))
		return
	}
	defer rows.Close()

	var episodes []models.Episode
	for rows.Next() {
		var e models.Episode
		if err := rows.Scan(&e.ID, &e.NamespaceID, &e.Content, &e.Embedding, &e.EmbeddingModel, &e.OccurredAt, &e.CreatedAt); err != nil {
			errs = append(errs, fmt.Sprintf("scan episode: %v", err))
			continue
		}
		episodes = append(episodes, e)
	}
	if err := rows.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("episode rows: %v", err))
		return
	}

	read = len(episodes)
	if read == 0 {
		return
	}

	// Cluster by vector similarity
	clusters := b.clusterEpisodes(episodes)

	var maxID int64
	processed := make(map[int64]bool)

	for _, cluster := range clusters {
		if ctx.Err() != nil {
			break
		}

		for _, e := range cluster {
			if e.ID > maxID {
				maxID = e.ID
			}
		}

		var texts []string
		var episodeIDs []int64
		for _, e := range cluster {
			texts = append(texts, e.Content)
			episodeIDs = append(episodeIDs, e.ID)
		}

		sf, err := b.reasoner.ReasonStructured(ctx, texts)
		llmCalls++
		if err != nil {
			errs = append(errs, fmt.Sprintf("reason structured: %v", err))
			continue
		}

		if sf.Summary == "" {
			for _, e := range cluster {
				processed[e.ID] = true
			}
			continue
		}

		// Embed the fact content
		vec, err := b.embedder.Embed(ctx, sf.Summary)
		if err != nil {
			errs = append(errs, fmt.Sprintf("embed fact: %v", err))
			continue
		}

		// Check for duplicate fact
		dupID, err := b.findDuplicateFact(ctx, nsID, vec)
		if err != nil {
			errs = append(errs, fmt.Sprintf("check duplicate: %v", err))
			continue
		}
		if dupID != 0 {
			// 중복은 버리는 게 아니라 기존 fact 에 대한 재관측으로 기록한다.
			if err := b.reinforceFact(ctx, dupID, cluster); err != nil {
				errs = append(errs, err.Error())
			}
			deduped++
			for _, e := range cluster {
				processed[e.ID] = true
			}
			continue
		}

		confidence := calculateConfidence(len(cluster), sf.Entity != "" && sf.Property != "")
		now := time.Now().UTC()

		var factID int64
		err = b.pool.QueryRow(ctx,
			`INSERT INTO facts (namespace_id, content, embedding, embedding_model, confidence, entity, property, value, valid_from)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			nsID, sf.Summary, pgvector.NewVector(vec), b.embedder.Model(), confidence,
			strPtrOrNull(sf.Entity), strPtrOrNull(sf.Property), strPtrOrNull(sf.Value), now,
		).Scan(&factID)
		if err != nil {
			errs = append(errs, fmt.Sprintf("insert fact: %v", err))
			continue
		}
		created++

		// Insert fact_sources
		for _, eid := range episodeIDs {
			if _, err := b.pool.Exec(ctx,
				"INSERT INTO fact_sources (fact_id, episode_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
				factID, eid,
			); err != nil {
				errs = append(errs, fmt.Sprintf("insert fact source: %v", err))
			}
		}

		// Stage 4: Contradiction detection
		newFact := &models.Fact{
			ID:          factID,
			NamespaceID: nsID,
			Content:     sf.Summary,
			Confidence:  confidence,
			Entity:      strPtrOrNull(sf.Entity),
			Property:    strPtrOrNull(sf.Property),
			Value:       strPtrOrNull(sf.Value),
		}
		cd, ca, contradictionErr := b.DetectContradictions(ctx, nsID, newFact)
		contradictionsFound += cd
		contradictionsAutoResolved += ca
		if contradictionErr != nil {
			errs = append(errs, fmt.Sprintf("detect contradictions: %v", contradictionErr))
		}

		for _, e := range cluster {
			processed[e.ID] = true
		}
	}

	// Only advance checkpoint if no errors occurred (bullet-proof: prevents losing episodes)
	if len(errs) == 0 && maxID > cp.LastEpisodeID {
		cp.LastEpisodeID = maxID
	}
	return
}

// clusterEpisodes groups episodes that describe the same thing, so each cluster can
// be synthesized into one fact.
//
// Two properties this deliberately guarantees:
//
//  1. Deterministic — episodes are visited in ascending ID order, so the same input
//     always yields the same clusters. The previous version iterated in whatever order
//     the caller supplied, which made seed selection (and therefore the resulting
//     facts) depend on query ordering.
//
//  2. Compared against the cluster centroid, not the seed. Comparing only to the seed
//     admits chaining: with A~B and B~C but A far from C, seeding on A pulled C in
//     anyway. Unrelated observations then got fused into a single fact. The centroid is
//     recomputed as members join, so a candidate must resemble the group as a whole.
func (b *Brain) clusterEpisodes(episodes []models.Episode) [][]models.Episode {
	if len(episodes) == 0 {
		return nil
	}

	ordered := make([]models.Episode, len(episodes))
	copy(ordered, episodes)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	threshold := float32(b.config.SimilarityThreshold)
	clustered := make(map[int64]bool, len(ordered))
	var clusters [][]models.Episode

	for _, seed := range ordered {
		if clustered[seed.ID] {
			continue
		}

		cluster := []models.Episode{seed}
		clustered[seed.ID] = true

		seedVec := seed.Embedding.Slice()
		if seedVec == nil {
			// 임베딩이 없으면 비교 기준이 없다. 단독 클러스터로 둔다.
			clusters = append(clusters, cluster)
			continue
		}

		centroid := make([]float32, len(seedVec))
		copy(centroid, seedVec)
		members := 1

		for _, candidate := range ordered {
			if clustered[candidate.ID] {
				continue
			}
			candVec := candidate.Embedding.Slice()
			if candVec == nil || len(candVec) != len(centroid) {
				continue
			}
			if cosineSimilarity(centroid, candVec) < threshold {
				continue
			}

			cluster = append(cluster, candidate)
			clustered[candidate.ID] = true

			// 중심을 이동 평균으로 갱신한다. 다음 후보는 갱신된 중심과 비교된다.
			members++
			for i := range centroid {
				centroid[i] += (candVec[i] - centroid[i]) / float32(members)
			}
		}

		clusters = append(clusters, cluster)
	}

	return clusters
}

// findDuplicateFact returns the id of an existing fact close enough to be considered
// the same statement, or 0 if there is none.
//
// Returns the id (not just a bool) so the caller can record the re-observation on the
// existing row. Previously this returned only a bool and the caller did nothing with
// it beyond skipping — see reinforceFact for why that mattered.
func (b *Brain) findDuplicateFact(ctx context.Context, nsID int64, vec []float32) (int64, error) {
	var id int64
	var score float32
	err := b.pool.QueryRow(ctx,
		`SELECT id, 1 - (embedding <=> $2) AS score FROM facts
		 WHERE namespace_id = $1 AND deleted_at IS NULL AND valid_until IS NULL AND embedding IS NOT NULL
		 ORDER BY embedding <=> $2 LIMIT 1`,
		nsID, pgvector.NewVector(vec),
	).Scan(&id, &score)
	if err != nil {
		if err == pgx.ErrNoRows {
			// 이 네임스페이스에 fact 가 아직 없다. 중복이 아니라는 정상 결론이다.
			return 0, nil
		}
		// 조회 실패를 "중복 아님"으로 삼키면 안 된다. 예전 코드는 `return false, nil` 이라
		// DB 오류·타임아웃이 그대로 중복 fact 생성으로 이어졌다 — 오류가 데이터 오염이 됐다.
		return 0, fmt.Errorf("find duplicate fact: %w", err)
	}
	if score >= float32(b.config.DedupThreshold) {
		return id, nil
	}
	return 0, nil
}

// reinforceFact records that an existing fact was observed again.
//
// Without this, re-observation was invisible: the dedup path simply skipped the
// cluster, so the existing row kept its original updated_at and confidence. Decay
// targets rows whose updated_at is older than the window, which meant a fact
// confirmed every single day aged out exactly like one never seen again —
// the opposite of what DecayConfidence documents.
func (b *Brain) reinforceFact(ctx context.Context, factID int64, episodes []models.Episode) error {
	// confidence 를 관측마다 남은 여유의 일부만 올린다. 1.0 을 넘지 않으면서
	// 반복 확인이 실제로 신뢰도에 반영되게 한다(감쇠 계수 0.95 를 상쇄하는 방향).
	if _, err := b.pool.Exec(ctx,
		`UPDATE facts
		 SET confidence = LEAST(1.0, confidence + (1 - confidence) * 0.25),
		     updated_at = now()
		 WHERE id = $1 AND deleted_at IS NULL`,
		factID,
	); err != nil {
		return fmt.Errorf("reinforce fact %d: %w", factID, err)
	}

	// 재관측 근거를 출처에 추가한다. 같은 사실을 뒷받침하는 episode 가 늘어난다.
	for _, e := range episodes {
		if _, err := b.pool.Exec(ctx,
			`INSERT INTO fact_sources (fact_id, episode_id) VALUES ($1, $2)
			 ON CONFLICT DO NOTHING`,
			factID, e.ID,
		); err != nil {
			return fmt.Errorf("link reinforcing episode %d to fact %d: %w", e.ID, factID, err)
		}
	}
	return nil
}

func calculateConfidence(observationCount int, hasStructuredFields bool) float32 {
	if observationCount == 0 {
		return 0.0
	}
	base := float32(observationCount) / float32(observationCount+2)
	if hasStructuredFields {
		base = base + (1-base)*0.3
	}
	if base > 1.0 {
		base = 1.0
	}
	return base
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

func strPtrOrNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- Stage 2: Facts -> Relationships ---

func (b *Brain) consolidateFactsToRelationships(ctx context.Context, nsID int64, cp *models.ConsolidationProgress) (count, llmCalls int, errs []string) {
	sql, args, err := b.queries.FetchFacts(nsID, cp.LastFactID, 50)
	if err != nil {
		errs = append(errs, fmt.Sprintf("build fetch facts: %v", err))
		return
	}

	rows, err := b.pool.Query(ctx, sql, args...)
	if err != nil {
		errs = append(errs, fmt.Sprintf("fetch facts: %v", err))
		return
	}
	defer rows.Close()

	var facts []models.Fact
	for rows.Next() {
		var f models.Fact
		if err := rows.Scan(&f.ID, &f.NamespaceID, &f.Content, &f.Embedding, &f.EmbeddingModel, &f.Confidence, &f.Entity, &f.Property, &f.Value, &f.ValidFrom, &f.ValidUntil, &f.CreatedAt, &f.UpdatedAt); err != nil {
			errs = append(errs, fmt.Sprintf("scan fact: %v", err))
			continue
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("fact rows: %v", err))
		return
	}

	if len(facts) == 0 {
		return
	}

	var maxID int64
	for _, fact := range facts {
		if ctx.Err() != nil {
			break
		}
		if fact.ID > maxID {
			maxID = fact.ID
		}

		rels, err := b.reasoner.ReasonRelationships(ctx, fact.Content)
		llmCalls++
		if err != nil {
			errs = append(errs, fmt.Sprintf("reason relationships fact %d: %v", fact.ID, err))
			continue
		}

		for _, rel := range rels {
			if rel.FromEntity == "" || rel.RelationType == "" || rel.ToEntity == "" {
				continue
			}
			if err := validateConfidence(rel.Confidence); err != nil {
				errs = append(errs, fmt.Sprintf("relationship confidence for fact %d: %v", fact.ID, err))
				continue
			}

			// Check for existing relationship from this fact
			exists, err := b.relationshipExists(ctx, nsID, rel.FromEntity, rel.RelationType, rel.ToEntity, fact.ID)
			if err != nil {
				errs = append(errs, fmt.Sprintf("check relationship: %v", err))
				continue
			}
			if exists {
				continue
			}

			_, err = b.pool.Exec(ctx,
				`INSERT INTO relationships (namespace_id, from_entity, relation_type, to_entity, confidence, source_fact_id)
				 VALUES ($1, $2, $3, $4, $5, $6)`,
				nsID, rel.FromEntity, rel.RelationType, rel.ToEntity, rel.Confidence, fact.ID,
			)
			if err != nil {
				errs = append(errs, fmt.Sprintf("insert relationship: %v", err))
				continue
			}
			count++
		}
	}

	// Only advance checkpoint if no errors occurred (bullet-proof: prevents losing facts)
	if len(errs) == 0 && maxID > cp.LastFactID {
		cp.LastFactID = maxID
	}
	return
}

func (b *Brain) relationshipExists(ctx context.Context, nsID int64, from, relType, to string, sourceFactID int64) (bool, error) {
	var id int64
	err := b.pool.QueryRow(ctx,
		`SELECT id FROM relationships
		 WHERE namespace_id = $1 AND from_entity = $2 AND relation_type = $3 AND to_entity = $4
		 AND source_fact_id = $5 AND deleted_at IS NULL LIMIT 1`,
		nsID, from, relType, to, sourceFactID,
	).Scan(&id)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("relationship lookup: %w", err)
	}
	return true, nil
}

// --- Stage 3.5: Facts -> Causal Links ---

func (b *Brain) consolidateFactsToCausalLinks(ctx context.Context, nsID int64, cp *models.ConsolidationProgress) (count, llmCalls int, errs []string) {
	// 전용 커서를 쓴다. 예전에는 cp.LastFactID 를 관계 추출 단계와 공유했는데,
	// 관계 단계가 먼저 실행되며 그 커서를 최신 fact 까지 밀어버려서
	// 여기서는 `WHERE id > <최신>` 이 되어 언제나 0건이 왔다.
	// 그 결과 아래 `len(facts) < 2` 에 항상 걸려 인과 추출이 사실상 죽어 있었다.
	sql, args, err := b.queries.FetchFacts(nsID, cp.LastCausalFactID, 30)
	if err != nil {
		errs = append(errs, fmt.Sprintf("build fetch facts for causal: %v", err))
		return
	}

	rows, err := b.pool.Query(ctx, sql, args...)
	if err != nil {
		errs = append(errs, fmt.Sprintf("fetch facts for causal: %v", err))
		return
	}
	defer rows.Close()

	var facts []models.Fact
	for rows.Next() {
		var f models.Fact
		if err := rows.Scan(&f.ID, &f.NamespaceID, &f.Content, &f.Embedding, &f.EmbeddingModel, &f.Confidence, &f.Entity, &f.Property, &f.Value, &f.ValidFrom, &f.ValidUntil, &f.CreatedAt, &f.UpdatedAt); err != nil {
			errs = append(errs, fmt.Sprintf("scan fact for causal: %v", err))
			continue
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("fact rows for causal: %v", err))
		return
	}

	if len(facts) < 2 {
		return
	}

	llmCalls++
	found, detectErrs := b.DetectCausalLinks(ctx, nsID, facts)
	count = found
	errs = append(errs, detectErrs...)

	// 오류가 없을 때만 커서를 전진시킨다(다른 단계와 동일 규칙 — 실패 시 fact 를 잃지 않는다).
	if len(errs) == 0 {
		var maxID int64
		for _, f := range facts {
			if f.ID > maxID {
				maxID = f.ID
			}
		}
		if maxID > cp.LastCausalFactID {
			cp.LastCausalFactID = maxID
		}
	}
	return
}

// --- Stage 3: Facts + Relationships -> Patterns ---

func (b *Brain) consolidateToPatterns(ctx context.Context, nsID int64, cp *models.ConsolidationProgress) (count, llmCalls int, errs []string) {
	// Fetch new facts since last pattern extraction
	factSQL, factArgs, err := b.queries.FetchFacts(nsID, cp.LastPatternFactID, 30)
	if err != nil {
		errs = append(errs, fmt.Sprintf("build fetch facts for patterns: %v", err))
		return
	}

	factRows, err := b.pool.Query(ctx, factSQL, factArgs...)
	if err != nil {
		errs = append(errs, fmt.Sprintf("fetch facts for patterns: %v", err))
		return
	}
	defer factRows.Close()

	var facts []models.Fact
	for factRows.Next() {
		var f models.Fact
		if err := factRows.Scan(&f.ID, &f.NamespaceID, &f.Content, &f.Embedding, &f.EmbeddingModel, &f.Confidence, &f.Entity, &f.Property, &f.Value, &f.ValidFrom, &f.ValidUntil, &f.CreatedAt, &f.UpdatedAt); err != nil {
			errs = append(errs, fmt.Sprintf("scan fact for pattern: %v", err))
			continue
		}
		facts = append(facts, f)
	}
	if err := factRows.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("fact rows for patterns: %v", err))
		return
	}

	if len(facts) == 0 {
		return
	}

	// Fetch new relationships since last pattern extraction
	relSQL, relArgs, err := b.queries.FetchRelationships(nsID, cp.LastPatternRelID, 50)
	if err != nil {
		errs = append(errs, fmt.Sprintf("build fetch rels for patterns: %v", err))
		return
	}

	relRows, err := b.pool.Query(ctx, relSQL, relArgs...)
	if err != nil {
		errs = append(errs, fmt.Sprintf("fetch rels for patterns: %v", err))
		return
	}
	defer relRows.Close()

	var rels []models.Relationship
	for relRows.Next() {
		var r models.Relationship
		if err := relRows.Scan(&r.ID, &r.NamespaceID, &r.FromEntity, &r.RelationType, &r.ToEntity, &r.Confidence, &r.SourceFactID, &r.CreatedAt); err != nil {
			errs = append(errs, fmt.Sprintf("scan rel for pattern: %v", err))
			continue
		}
		rels = append(rels, r)
	}
	if err := relRows.Err(); err != nil {
		errs = append(errs, fmt.Sprintf("rel rows for patterns: %v", err))
	}

	if len(rels) == 0 && len(facts) < 3 {
		// Not enough data for pattern extraction
		b.updatePatternCheckpoint(ctx, cp, facts, rels, len(errs) == 0)
		return
	}
	factIDs := make(map[int64]struct{}, len(facts))
	for _, fact := range facts {
		factIDs[fact.ID] = struct{}{}
	}
	relIDs := make(map[int64]struct{}, len(rels))
	for _, rel := range rels {
		relIDs[rel.ID] = struct{}{}
	}

	// Call reasoner for pattern extraction
	patterns, err := b.reasoner.ReasonPatterns(ctx, facts, rels)
	llmCalls++
	if err != nil {
		errs = append(errs, fmt.Sprintf("reason patterns: %v", err))
		// Don't update checkpoint on error
		return
	}

	for _, p := range patterns {
		if err := validateContent(p.Content); err != nil {
			errs = append(errs, fmt.Sprintf("pattern content: %v", err))
			continue
		}
		if err := validateConfidence(p.CoherenceScore); err != nil {
			errs = append(errs, fmt.Sprintf("pattern coherence: %v", err))
			continue
		}
		validSourceIDs := true
		for _, fid := range p.SourceFactIDs {
			if _, ok := factIDs[fid]; !ok {
				validSourceIDs = false
				break
			}
		}
		if validSourceIDs {
			for _, rid := range p.SourceRelIDs {
				if _, ok := relIDs[rid]; !ok {
					validSourceIDs = false
					break
				}
			}
		}
		if !validSourceIDs {
			errs = append(errs, "pattern references a fact or relationship outside the consolidation batch")
			continue
		}

		// Confidence = min(source confidences) * coherence_score
		confidence := p.CoherenceScore
		if len(p.SourceFactIDs) > 0 || len(p.SourceRelIDs) > 0 {
			minConf := float32(1.0)
			for _, fid := range p.SourceFactIDs {
				for _, f := range facts {
					if f.ID == fid && f.Confidence < minConf {
						minConf = f.Confidence
					}
				}
			}
			for _, rid := range p.SourceRelIDs {
				for _, r := range rels {
					if r.ID == rid && r.Confidence < minConf {
						minConf = r.Confidence
					}
				}
			}
			confidence = minConf * p.CoherenceScore
		}

		// If no source IDs provided, use all facts/rels as sources
		sourceFactIDs := p.SourceFactIDs
		sourceRelIDs := p.SourceRelIDs
		if len(sourceFactIDs) == 0 || len(sourceRelIDs) == 0 {
			sourceFactIDs = make([]int64, len(facts))
			for i, f := range facts {
				sourceFactIDs[i] = f.ID
			}
			sourceRelIDs = make([]int64, len(rels))
			for i, r := range rels {
				sourceRelIDs[i] = r.ID
			}
		}

		_, err := b.pool.Exec(ctx,
			`INSERT INTO patterns (namespace_id, content, confidence, source_fact_ids, source_rel_ids, coherence_score)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			nsID, p.Content, confidence, sourceFactIDs, sourceRelIDs, p.CoherenceScore,
		)
		if err != nil {
			errs = append(errs, fmt.Sprintf("insert pattern: %v", err))
			continue
		}
		count++
	}

	// Only update checkpoint if no errors occurred (bullet-proof: prevents losing patterns)
	b.updatePatternCheckpoint(ctx, cp, facts, rels, len(errs) == 0)
	return
}

func (b *Brain) updatePatternCheckpoint(ctx context.Context, cp *models.ConsolidationProgress, facts []models.Fact, rels []models.Relationship, success bool) {
	// Only advance checkpoint if no errors occurred
	if !success {
		return
	}
	for _, f := range facts {
		if f.ID > cp.LastPatternFactID {
			cp.LastPatternFactID = f.ID
		}
	}
	for _, r := range rels {
		if r.ID > cp.LastPatternRelID {
			cp.LastPatternRelID = r.ID
		}
	}
}
