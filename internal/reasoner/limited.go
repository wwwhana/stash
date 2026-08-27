package reasoner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/textbudget"
)

const maxAdaptiveReasonerSplits = 8

// Limited protects an OpenAI-compatible reasoner from model-specific context
// limits. The wrapped reasoner still owns prompts and validation; this layer
// only chooses smaller evidence batches and combines the independent results.
// If the provider does not publish a limit, a context-length error triggers an
// adaptive split and retry rather than being returned as a permanent failure.
type Limited struct {
	inner          Reasoner
	maxInputBytes  int
	reservedTokens int
}

// NewLimited wraps a reasoner with a model context budget. ContextTokens is
// the model's full window; ReservedTokens covers system instructions, JSON
// output, and the caller's safety margin. UTF-8 bytes are used as a
// conservative token upper bound because compatible providers use different
// tokenizers.
func NewLimited(inner Reasoner, contextTokens, reservedTokens int) *Limited {
	inputTokens := contextTokens
	if inputTokens > 0 {
		inputTokens -= reservedTokens
		if inputTokens < 1 {
			inputTokens = 1
		}
	}
	return &Limited{
		inner:          inner,
		maxInputBytes:  textbudget.BytesForTokens(inputTokens),
		reservedTokens: maxInt(0, reservedTokens),
	}
}

func (l *Limited) MaxInputBytes() int { return l.maxInputBytes }

// ModelName forwards the optional model identity used by work-plan reviews.
func (l *Limited) ModelName() string {
	if named, ok := l.inner.(interface{ ModelName() string }); ok {
		return named.ModelName()
	}
	return ""
}

// ValidateWorkPlan forwards semantic validation when the wrapped reasoner
// supports it. Oversized plans are reviewed component-by-component and the
// findings are merged by stable IDs.
func (l *Limited) ValidateWorkPlan(ctx context.Context, plan models.WorkPlan) (*WorkPlanValidationResult, error) {
	validator, ok := l.inner.(WorkPlanValidator)
	if !ok {
		return nil, errors.New("reasoner: work plan validation is not supported")
	}
	plans := l.splitWorkPlans(plan)
	if len(plans) == 1 {
		result, err := validator.ValidateWorkPlan(ctx, plans[0])
		if err == nil || !textbudget.IsContextLimitError(err) {
			return result, err
		}
		// A provider may count prompt scaffolding differently from our byte
		// estimate. Retry with a deterministic component split below.
		plans = splitWorkPlanInHalf(plan)
	}
	if len(plans) <= 1 {
		return nil, fmt.Errorf("reasoner: work plan exceeds the configured model context")
	}

	merged := &WorkPlanValidationResult{}
	seen := make(map[string]struct{})
	for i, partial := range plans {
		result, err := validator.ValidateWorkPlan(ctx, partial)
		if err != nil {
			if textbudget.IsContextLimitError(err) && len(partial.Components) > 1 && i < maxAdaptiveReasonerSplits {
				return l.ValidateWorkPlan(ctx, models.WorkPlan{Components: append([]models.WorkPlanComponent(nil), partial.Components...), Decisions: partial.Decisions})
			}
			return nil, fmt.Errorf("reasoner: validate work-plan chunk %d/%d: %w", i+1, len(plans), err)
		}
		if result == nil {
			continue
		}
		if merged.Summary == "" {
			merged.Summary = strings.TrimSpace(result.Summary)
		} else if summary := strings.TrimSpace(result.Summary); summary != "" {
			merged.Summary += " " + summary
		}
		for _, finding := range result.Findings {
			key := fmt.Sprintf("%s/%d/%d/%d/%s", finding.Code, finding.ComponentID, finding.RelatedComponentID, finding.TaskID, finding.Message)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged.Findings = append(merged.Findings, finding)
		}
	}
	if merged.Summary == "" {
		merged.Summary = "The work plan was reviewed in model-sized sections."
	}
	return merged, nil
}

func (l *Limited) ReasonStructured(ctx context.Context, texts []string) (*StructuredFact, error) {
	return l.reasonStructured(ctx, texts, 0)
}

func (l *Limited) reasonStructured(ctx context.Context, texts []string, depth int) (*StructuredFact, error) {
	chunks := textbudget.SplitStrings(texts, l.maxInputBytes)
	if len(chunks) == 1 {
		result, err := l.inner.ReasonStructured(ctx, chunks[0])
		if err == nil {
			return result, nil
		}
		if !textbudget.IsContextLimitError(err) || depth >= maxAdaptiveReasonerSplits {
			return nil, err
		}
		return l.withBudget(l.nextBudget(err, shrinkBudget(chunks[0], l.maxInputBytes))).reasonStructured(ctx, texts, depth+1)
	}

	var summaries []string
	for i, chunk := range chunks {
		result, err := l.inner.ReasonStructured(ctx, chunk)
		if err != nil {
			if textbudget.IsContextLimitError(err) && depth < maxAdaptiveReasonerSplits {
				return l.withBudget(l.nextBudget(err, shrinkBudget(chunk, l.maxInputBytes))).reasonStructured(ctx, texts, depth+1)
			}
			return nil, fmt.Errorf("reasoner: structured chunk %d/%d: %w", i+1, len(chunks), err)
		}
		if result == nil {
			continue
		}
		if result.Summary != "" {
			summaries = append(summaries, result.Summary)
		} else if summary := structuredFactText(result); summary != "" {
			summaries = append(summaries, summary)
		}
	}
	if len(summaries) == 0 {
		return nil, nil
	}
	if depth >= maxAdaptiveReasonerSplits {
		return l.inner.ReasonStructured(ctx, summaries[:1])
	}
	return l.reasonStructured(ctx, summaries, depth+1)
}

func (l *Limited) withBudget(maxBytes int) *Limited {
	copyLimited := *l
	copyLimited.maxInputBytes = maxBytes
	return &copyLimited
}

// nextBudget prefers the exact limit reported by the provider. If the error
// does not contain a limit, or the reported limit is not smaller than the
// current budget, the caller's deterministic fallback is used.
func (l *Limited) nextBudget(err error, fallback int) int {
	if budget, ok := textbudget.InputBudgetFromContextError(err, l.reservedTokens); ok &&
		(l.maxInputBytes <= 0 || budget < l.maxInputBytes) {
		return budget
	}
	return fallback
}

func shrinkBudget(values []string, current int) int {
	if current > 1 {
		return current / 2
	}
	used := 0
	for _, value := range values {
		used += len(value)
	}
	if used < 2 {
		return 1
	}
	return used / 2
}

func shrinkBudgetText(value string, current int) int {
	return shrinkBudget([]string{value}, current)
}

func shrinkPatternBudget(batch patternBatch, current int) int {
	if current > 1 {
		return current / 2
	}
	used := 0
	for _, fact := range batch.facts {
		used += patternFactSize(fact)
	}
	for _, rel := range batch.relationships {
		used += patternRelSize(rel)
	}
	return maxInt(1, used/2)
}

func shrinkFactsBudget(facts []models.Fact, current int) int {
	if current > 1 {
		return current / 2
	}
	used := 0
	for _, fact := range facts {
		used += causalFactSize(fact)
	}
	return maxInt(1, used/2)
}

func shrinkGoalFactsBudget(batch goalFactBatch, current int) int {
	if current > 1 {
		return current / 2
	}
	used := 0
	for _, goal := range batch.goals {
		used += goalSize(goal)
	}
	for _, fact := range batch.facts {
		used += goalFactSize(fact)
	}
	return maxInt(1, used/2)
}

func shrinkFailureEvidenceBudget(batch failureEvidenceBatch, current int) int {
	if current > 1 {
		return current / 2
	}
	used := 0
	for _, failure := range batch.failures {
		used += failureSize(failure)
	}
	for _, evidence := range batch.evidence {
		used += len(evidence)
	}
	return maxInt(1, used/2)
}

func shrinkHypothesisFactsBudget(batch hypothesisFactBatch, current int) int {
	if current > 1 {
		return current / 2
	}
	used := 0
	for _, hypothesis := range batch.hypotheses {
		used += hypothesisSize(hypothesis)
	}
	for _, fact := range batch.facts {
		used += goalFactSize(fact)
	}
	return maxInt(1, used/2)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

/*
	The methods below intentionally call the wrapped implementation directly.
	When a provider counts prompt scaffolding differently from our conservative
	budget, they restart with a smaller budget instead of repeating the same
	oversized request.
*/

func (l *Limited) ReasonRelationships(ctx context.Context, factContent string) ([]*StructuredRelationship, error) {
	return l.reasonRelationships(ctx, factContent, 0)
}

func (l *Limited) reasonRelationships(ctx context.Context, factContent string, depth int) ([]*StructuredRelationship, error) {
	parts := textbudget.SplitText(factContent, l.maxInputBytes)
	if len(parts) == 1 {
		result, err := l.inner.ReasonRelationships(ctx, factContent)
		if err == nil || !textbudget.IsContextLimitError(err) {
			return result, err
		}
		if depth >= maxAdaptiveReasonerSplits {
			return nil, err
		}
		return l.withBudget(l.nextBudget(err, shrinkBudgetText(factContent, l.maxInputBytes))).reasonRelationships(ctx, factContent, depth+1)
	}
	return l.relationshipChunks(ctx, parts, factContent, depth)
}

func (l *Limited) relationshipChunks(ctx context.Context, parts []string, original string, depth int) ([]*StructuredRelationship, error) {
	seen := make(map[string]struct{})
	var result []*StructuredRelationship
	for i, part := range parts {
		items, err := l.inner.ReasonRelationships(ctx, part)
		if err != nil {
			if textbudget.IsContextLimitError(err) && depth < maxAdaptiveReasonerSplits && len(part) > 1 {
				return l.withBudget(l.nextBudget(err, shrinkBudgetText(part, l.maxInputBytes))).reasonRelationships(ctx, original, depth+1)
			}
			return nil, fmt.Errorf("reasoner: relationship chunk %d/%d: %w", i+1, len(parts), err)
		}
		for _, item := range items {
			key := fmt.Sprintf("%s/%s/%s", item.FromEntity, item.RelationType, item.ToEntity)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result, nil
}

type patternBatch struct {
	facts         []models.Fact
	relationships []models.Relationship
}

func (l *Limited) ReasonPatterns(ctx context.Context, facts []models.Fact, relationships []models.Relationship) ([]*StructuredPattern, error) {
	return l.reasonPatterns(ctx, facts, relationships, 0)
}

func (l *Limited) reasonPatterns(ctx context.Context, facts []models.Fact, relationships []models.Relationship, depth int) ([]*StructuredPattern, error) {
	batches := l.patternBatches(facts, relationships)
	if len(batches) == 1 {
		result, err := l.inner.ReasonPatterns(ctx, batches[0].facts, batches[0].relationships)
		if err == nil || !textbudget.IsContextLimitError(err) {
			return result, err
		}
		if depth >= maxAdaptiveReasonerSplits {
			return nil, err
		}
		return l.withBudget(l.nextBudget(err, shrinkPatternBudget(batches[0], l.maxInputBytes))).reasonPatterns(ctx, facts, relationships, depth+1)
	}
	seen := make(map[string]struct{})
	var result []*StructuredPattern
	for i, batch := range batches {
		if len(batch.facts) == 0 {
			continue
		}
		items, err := l.inner.ReasonPatterns(ctx, batch.facts, batch.relationships)
		if err != nil {
			if textbudget.IsContextLimitError(err) && depth < maxAdaptiveReasonerSplits {
				return l.withBudget(l.nextBudget(err, shrinkPatternBudget(batch, l.maxInputBytes))).reasonPatterns(ctx, facts, relationships, depth+1)
			}
			return nil, fmt.Errorf("reasoner: pattern chunk %d/%d: %w", i+1, len(batches), err)
		}
		for _, item := range items {
			key := fmt.Sprintf("%s/%v/%v", item.Content, item.SourceFactIDs, item.SourceRelIDs)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result, nil
}

func (l *Limited) ReasonContradiction(ctx context.Context, entity, property, oldValue, newValue string) (*ContradictionResult, error) {
	return l.reasonContradiction(ctx, entity, property, oldValue, newValue, 0)
}

func (l *Limited) reasonContradiction(ctx context.Context, entity, property, oldValue, newValue string, depth int) (*ContradictionResult, error) {
	parts := splitContradictionValues(oldValue, newValue, l.maxInputBytes)
	if len(parts) == 1 {
		result, err := l.inner.ReasonContradiction(ctx, entity, property, oldValue, newValue)
		if err == nil || !textbudget.IsContextLimitError(err) {
			return result, err
		}
		if depth >= maxAdaptiveReasonerSplits {
			return nil, err
		}
		return l.withBudget(l.nextBudget(err, shrinkBudget([]string{oldValue, newValue}, l.maxInputBytes))).reasonContradiction(ctx, entity, property, oldValue, newValue, depth+1)
	}

	results := make([]*ContradictionResult, 0, len(parts))
	for i, part := range parts {
		result, err := l.inner.ReasonContradiction(ctx, entity, property, part.oldValue, part.newValue)
		if err != nil {
			if textbudget.IsContextLimitError(err) && depth < maxAdaptiveReasonerSplits {
				return l.withBudget(l.nextBudget(err, shrinkBudget([]string{part.oldValue, part.newValue}, l.maxInputBytes))).reasonContradiction(ctx, entity, property, oldValue, newValue, depth+1)
			}
			return nil, fmt.Errorf("reasoner: contradiction chunk %d/%d: %w", i+1, len(parts), err)
		}
		if result != nil {
			results = append(results, result)
		}
	}
	return mergeContradictions(results), nil
}

type contradictionPart struct {
	oldValue string
	newValue string
}

func splitContradictionValues(oldValue, newValue string, maxBytes int) []contradictionPart {
	if maxBytes <= 0 {
		return []contradictionPart{{oldValue: oldValue, newValue: newValue}}
	}
	// Keep the two values in separate halves so each request has room for the
	// entity, property, and the wrapped prompt's instructions.
	valueBudget := maxInt(1, maxBytes/2)
	oldParts := textbudget.SplitText(oldValue, valueBudget)
	newParts := textbudget.SplitText(newValue, valueBudget)
	if len(oldParts) == 1 && len(newParts) == 1 {
		return []contradictionPart{{oldValue: oldValue, newValue: newValue}}
	}
	count := len(oldParts)
	if len(newParts) > count {
		count = len(newParts)
	}
	parts := make([]contradictionPart, 0, count)
	for i := 0; i < count; i++ {
		parts = append(parts, contradictionPart{
			oldValue: oldParts[minIndex(i, len(oldParts))],
			newValue: newParts[minIndex(i, len(newParts))],
		})
	}
	return parts
}

func mergeContradictions(results []*ContradictionResult) *ContradictionResult {
	if len(results) == 0 {
		return nil
	}
	merged := &ContradictionResult{Classification: ClassificationCompatible}
	var explanations []string
	for _, result := range results {
		if result == nil {
			continue
		}
		if contradictionRank(result.Classification) > contradictionRank(merged.Classification) {
			merged.Classification = result.Classification
		}
		if result.Confidence > merged.Confidence {
			merged.Confidence = result.Confidence
		}
		if explanation := strings.TrimSpace(result.Explanation); explanation != "" {
			explanations = append(explanations, explanation)
		}
	}
	merged.Explanation = strings.Join(explanations, " ")
	return merged
}

func contradictionRank(classification ContradictionClassification) int {
	switch classification {
	case ClassificationContradiction:
		return 3
	case ClassificationReplacement:
		return 2
	case ClassificationCompatible:
		return 1
	default:
		return 0
	}
}

func (l *Limited) ReasonCausalLinks(ctx context.Context, facts []models.Fact) ([]*StructuredCausalLink, error) {
	return l.reasonCausalLinks(ctx, facts, 0)
}

func (l *Limited) reasonCausalLinks(ctx context.Context, facts []models.Fact, depth int) ([]*StructuredCausalLink, error) {
	chunks := l.factBatches(facts, l.maxInputBytes, causalFactSize)
	if len(chunks) == 1 {
		result, err := l.inner.ReasonCausalLinks(ctx, chunks[0])
		if err == nil || !textbudget.IsContextLimitError(err) {
			return result, err
		}
		if depth >= maxAdaptiveReasonerSplits {
			return nil, err
		}
		return l.withBudget(l.nextBudget(err, shrinkFactsBudget(chunks[0], l.maxInputBytes))).reasonCausalLinks(ctx, facts, depth+1)
	}
	seen := make(map[string]struct{})
	var result []*StructuredCausalLink
	for i, chunk := range chunks {
		if len(chunk) < 2 {
			continue
		}
		items, err := l.inner.ReasonCausalLinks(ctx, chunk)
		if err != nil {
			if textbudget.IsContextLimitError(err) && depth < maxAdaptiveReasonerSplits {
				return l.withBudget(l.nextBudget(err, shrinkFactsBudget(chunk, l.maxInputBytes))).reasonCausalLinks(ctx, facts, depth+1)
			}
			return nil, fmt.Errorf("reasoner: causal chunk %d/%d: %w", i+1, len(chunks), err)
		}
		for _, item := range items {
			key := fmt.Sprintf("%d/%d", item.CauseFactID, item.EffectFactID)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result, nil
}

func (l *Limited) ReasonGoalProgress(ctx context.Context, goals []models.Goal, facts []models.Fact) ([]*GoalProgressAssessment, error) {
	return l.reasonGoalProgress(ctx, goals, facts, 0)
}

func (l *Limited) reasonGoalProgress(ctx context.Context, goals []models.Goal, facts []models.Fact, depth int) ([]*GoalProgressAssessment, error) {
	batches := l.goalFactBatches(goals, facts)
	if len(batches) == 1 {
		result, err := l.inner.ReasonGoalProgress(ctx, batches[0].goals, batches[0].facts)
		if err == nil || !textbudget.IsContextLimitError(err) {
			return result, err
		}
		if depth >= maxAdaptiveReasonerSplits {
			return nil, err
		}
		return l.withBudget(l.nextBudget(err, shrinkGoalFactsBudget(batches[0], l.maxInputBytes))).reasonGoalProgress(ctx, goals, facts, depth+1)
	}
	var result []*GoalProgressAssessment
	seen := make(map[string]struct{})
	for i, batch := range batches {
		items, err := l.inner.ReasonGoalProgress(ctx, batch.goals, batch.facts)
		if err != nil {
			if textbudget.IsContextLimitError(err) && depth < maxAdaptiveReasonerSplits {
				return l.withBudget(l.nextBudget(err, shrinkGoalFactsBudget(batch, l.maxInputBytes))).reasonGoalProgress(ctx, goals, facts, depth+1)
			}
			return nil, fmt.Errorf("reasoner: goal-progress chunk %d/%d: %w", i+1, len(batches), err)
		}
		for _, item := range items {
			key := fmt.Sprintf("%d/%s/%s", item.GoalID, item.Assessment, item.Note)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result, nil
}

func (l *Limited) ReasonFailurePatterns(ctx context.Context, failures []models.Failure, evidence []string) ([]*FailurePatternResult, error) {
	return l.reasonFailurePatterns(ctx, failures, evidence, 0)
}

func (l *Limited) reasonFailurePatterns(ctx context.Context, failures []models.Failure, evidence []string, depth int) ([]*FailurePatternResult, error) {
	batches := l.failureEvidenceBatches(failures, evidence)
	if len(batches) == 1 {
		result, err := l.inner.ReasonFailurePatterns(ctx, batches[0].failures, batches[0].evidence)
		if err == nil || !textbudget.IsContextLimitError(err) {
			return result, err
		}
		if depth >= maxAdaptiveReasonerSplits {
			return nil, err
		}
		return l.withBudget(l.nextBudget(err, shrinkFailureEvidenceBudget(batches[0], l.maxInputBytes))).reasonFailurePatterns(ctx, failures, evidence, depth+1)
	}
	var result []*FailurePatternResult
	seen := make(map[string]struct{})
	for i, batch := range batches {
		items, err := l.inner.ReasonFailurePatterns(ctx, batch.failures, batch.evidence)
		if err != nil {
			if textbudget.IsContextLimitError(err) && depth < maxAdaptiveReasonerSplits {
				return l.withBudget(l.nextBudget(err, shrinkFailureEvidenceBudget(batch, l.maxInputBytes))).reasonFailurePatterns(ctx, failures, evidence, depth+1)
			}
			return nil, fmt.Errorf("reasoner: failure-pattern chunk %d/%d: %w", i+1, len(batches), err)
		}
		for _, item := range items {
			key := fmt.Sprintf("%s/%d/%s/%s", item.Type, item.FailureID, item.Evidence, item.PatternFact)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result, nil
}

func (l *Limited) ReasonHypothesisEvidence(ctx context.Context, hypotheses []models.Hypothesis, facts []models.Fact) ([]*HypothesisEvidenceResult, error) {
	return l.reasonHypothesisEvidence(ctx, hypotheses, facts, 0)
}

func (l *Limited) reasonHypothesisEvidence(ctx context.Context, hypotheses []models.Hypothesis, facts []models.Fact, depth int) ([]*HypothesisEvidenceResult, error) {
	batches := l.hypothesisFactBatches(hypotheses, facts)
	if len(batches) == 1 {
		result, err := l.inner.ReasonHypothesisEvidence(ctx, batches[0].hypotheses, batches[0].facts)
		if err == nil || !textbudget.IsContextLimitError(err) {
			return result, err
		}
		if depth >= maxAdaptiveReasonerSplits {
			return nil, err
		}
		return l.withBudget(l.nextBudget(err, shrinkHypothesisFactsBudget(batches[0], l.maxInputBytes))).reasonHypothesisEvidence(ctx, hypotheses, facts, depth+1)
	}
	var result []*HypothesisEvidenceResult
	seen := make(map[string]struct{})
	for i, batch := range batches {
		items, err := l.inner.ReasonHypothesisEvidence(ctx, batch.hypotheses, batch.facts)
		if err != nil {
			if textbudget.IsContextLimitError(err) && depth < maxAdaptiveReasonerSplits {
				return l.withBudget(l.nextBudget(err, shrinkHypothesisFactsBudget(batch, l.maxInputBytes))).reasonHypothesisEvidence(ctx, hypotheses, facts, depth+1)
			}
			return nil, fmt.Errorf("reasoner: hypothesis-evidence chunk %d/%d: %w", i+1, len(batches), err)
		}
		for _, item := range items {
			key := fmt.Sprintf("%d/%s/%s", item.HypothesisID, item.Verdict, item.Reasoning)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, item)
		}
	}
	return result, nil
}

// --- batch sizing helpers ---

type goalFactBatch struct {
	goals []models.Goal
	facts []models.Fact
}

type failureEvidenceBatch struct {
	failures []models.Failure
	evidence []string
}

type hypothesisFactBatch struct {
	hypotheses []models.Hypothesis
	facts      []models.Fact
}

func (l *Limited) factBatches(facts []models.Fact, max int, size func(models.Fact) int) [][]models.Fact {
	if max <= 0 {
		return [][]models.Fact{append([]models.Fact(nil), facts...)}
	}
	expanded := make([]models.Fact, 0, len(facts))
	for _, fact := range facts {
		for _, part := range textbudget.SplitText(fact.Content, max) {
			copyFact := fact
			copyFact.Content = part
			expanded = append(expanded, copyFact)
		}
	}
	return chunkItems(expanded, max, size)
}

func causalFactSize(f models.Fact) int {
	return len(fmt.Sprintf("[Fact %d] %s", f.ID, f.Content))
}

func patternFactSize(f models.Fact) int {
	return len(fmt.Sprintf("[Fact %d] %s (confidence: %.2f)", f.ID, f.Content, f.Confidence))
}

func patternRelSize(r models.Relationship) int {
	return len(fmt.Sprintf("[Rel %d] %s --%s--> %s (confidence: %.2f)", r.ID, r.FromEntity, r.RelationType, r.ToEntity, r.Confidence))
}

func (l *Limited) patternBatches(facts []models.Fact, relationships []models.Relationship) []patternBatch {
	if l.maxInputBytes <= 0 {
		return []patternBatch{{facts: append([]models.Fact(nil), facts...), relationships: append([]models.Relationship(nil), relationships...)}}
	}
	// Leave room for both sections of the prompt. The wrapped implementation
	// adds headings and instructions on top of these records.
	sectionBudget := maxInt(1, l.maxInputBytes/2)
	factBatches := l.factBatches(facts, sectionBudget, patternFactSize)
	relBatches := chunkItems(relationships, sectionBudget, patternRelSize)
	if len(factBatches) == 0 {
		return nil
	}
	if len(relBatches) == 0 {
		result := make([]patternBatch, 0, len(factBatches))
		for _, batch := range factBatches {
			result = append(result, patternBatch{facts: batch})
		}
		return result
	}
	// Repeat the shorter section when necessary so no fact or relationship
	// chunk disappears just because the other section produced more chunks.
	count := len(factBatches)
	if len(relBatches) > count {
		count = len(relBatches)
	}
	result := make([]patternBatch, 0, count)
	for i := 0; i < count; i++ {
		batch := patternBatch{
			facts:         factBatches[minIndex(i, len(factBatches))],
			relationships: relBatches[minIndex(i, len(relBatches))],
		}
		result = append(result, batch)
	}
	return result
}

func minIndex(index, length int) int {
	if length <= 0 {
		return 0
	}
	if index >= length {
		return length - 1
	}
	return index
}

func (l *Limited) goalFactBatches(goals []models.Goal, facts []models.Fact) []goalFactBatch {
	if l.maxInputBytes <= 0 {
		return []goalFactBatch{{goals: append([]models.Goal(nil), goals...), facts: append([]models.Fact(nil), facts...)}}
	}
	sectionBudget := maxInt(1, l.maxInputBytes/2)
	goalParts := splitGoalParts(goals, sectionBudget)
	factParts := l.factBatches(facts, sectionBudget, goalFactSize)
	return pairGoalFacts(goalParts, factParts, l.maxInputBytes)
}

func goalFactSize(f models.Fact) int { return len(fmt.Sprintf("[Fact %d] %s", f.ID, f.Content)) }

func goalSize(g models.Goal) int { return len(fmt.Sprintf("[Goal %d] %s", g.ID, g.Content)) }

func splitGoalParts(goals []models.Goal, max int) [][]models.Goal {
	expanded := make([]models.Goal, 0, len(goals))
	for _, goal := range goals {
		for _, part := range textbudget.SplitText(goal.Content, max) {
			copyGoal := goal
			copyGoal.Content = part
			expanded = append(expanded, copyGoal)
		}
	}
	return chunkItems(expanded, max, goalSize)
}

func pairGoalFacts(goals [][]models.Goal, facts [][]models.Fact, max int) []goalFactBatch {
	if len(goals) == 0 || len(facts) == 0 {
		return nil
	}
	if len(goals) == 1 {
		result := make([]goalFactBatch, 0, len(facts))
		for _, factChunk := range facts {
			result = append(result, goalFactBatch{goals: goals[0], facts: factChunk})
		}
		return result
	}
	if len(facts) == 1 {
		result := make([]goalFactBatch, 0, len(goals))
		for _, goalChunk := range goals {
			result = append(result, goalFactBatch{goals: goalChunk, facts: facts[0]})
		}
		return result
	}
	count := len(goals)
	if len(facts) > count {
		count = len(facts)
	}
	result := make([]goalFactBatch, 0, count)
	for i := 0; i < count; i++ {
		result = append(result, goalFactBatch{
			goals: goals[minIndex(i, len(goals))],
			facts: facts[minIndex(i, len(facts))],
		})
	}
	return result
}

func (l *Limited) failureEvidenceBatches(failures []models.Failure, evidence []string) []failureEvidenceBatch {
	if l.maxInputBytes <= 0 {
		return []failureEvidenceBatch{{failures: append([]models.Failure(nil), failures...), evidence: append([]string(nil), evidence...)}}
	}
	sectionBudget := maxInt(1, l.maxInputBytes/2)
	failureParts := splitFailureParts(failures, sectionBudget)
	evidenceParts := textbudget.SplitStrings(evidence, sectionBudget)
	return pairFailureEvidence(failureParts, evidenceParts)
}

func failureSize(f models.Failure) int {
	return len(fmt.Sprintf("[Failure %d] What: %s | Why: %s | Lesson: %s", f.ID, f.Content, f.Reason, f.Lesson))
}

func splitFailureParts(failures []models.Failure, max int) [][]models.Failure {
	expanded := make([]models.Failure, 0, len(failures))
	for _, failure := range failures {
		for _, part := range textbudget.SplitText(failure.Content, max) {
			copyFailure := failure
			copyFailure.Content = part
			expanded = append(expanded, copyFailure)
		}
	}
	return chunkItems(expanded, max, failureSize)
}

func pairFailureEvidence(failures [][]models.Failure, evidence [][]string) []failureEvidenceBatch {
	if len(failures) == 0 || len(evidence) == 0 {
		return nil
	}
	if len(failures) == 1 {
		result := make([]failureEvidenceBatch, 0, len(evidence))
		for _, part := range evidence {
			result = append(result, failureEvidenceBatch{failures: failures[0], evidence: part})
		}
		return result
	}
	if len(evidence) == 1 {
		result := make([]failureEvidenceBatch, 0, len(failures))
		for _, part := range failures {
			result = append(result, failureEvidenceBatch{failures: part, evidence: evidence[0]})
		}
		return result
	}
	count := len(failures)
	if len(evidence) > count {
		count = len(evidence)
	}
	result := make([]failureEvidenceBatch, 0, count)
	for i := 0; i < count; i++ {
		result = append(result, failureEvidenceBatch{
			failures: failures[minIndex(i, len(failures))],
			evidence: evidence[minIndex(i, len(evidence))],
		})
	}
	return result
}

func (l *Limited) hypothesisFactBatches(hypotheses []models.Hypothesis, facts []models.Fact) []hypothesisFactBatch {
	if l.maxInputBytes <= 0 {
		return []hypothesisFactBatch{{hypotheses: append([]models.Hypothesis(nil), hypotheses...), facts: append([]models.Fact(nil), facts...)}}
	}
	sectionBudget := maxInt(1, l.maxInputBytes/2)
	hypothesisParts := splitHypothesisParts(hypotheses, sectionBudget)
	factParts := l.factBatches(facts, sectionBudget, goalFactSize)
	if len(hypothesisParts) == 0 || len(factParts) == 0 {
		return nil
	}
	if len(hypothesisParts) == 1 {
		result := make([]hypothesisFactBatch, 0, len(factParts))
		for _, part := range factParts {
			result = append(result, hypothesisFactBatch{hypotheses: hypothesisParts[0], facts: part})
		}
		return result
	}
	if len(factParts) == 1 {
		result := make([]hypothesisFactBatch, 0, len(hypothesisParts))
		for _, part := range hypothesisParts {
			result = append(result, hypothesisFactBatch{hypotheses: part, facts: factParts[0]})
		}
		return result
	}
	count := len(hypothesisParts)
	if len(factParts) > count {
		count = len(factParts)
	}
	result := make([]hypothesisFactBatch, 0, count)
	for i := 0; i < count; i++ {
		result = append(result, hypothesisFactBatch{
			hypotheses: hypothesisParts[minIndex(i, len(hypothesisParts))],
			facts:      factParts[minIndex(i, len(factParts))],
		})
	}
	return result
}

func hypothesisSize(h models.Hypothesis) int {
	return len(fmt.Sprintf("[Hypothesis %d] (status: %s, confidence: %.2f) %s", h.ID, h.Status, h.Confidence, h.Content))
}

func splitHypothesisParts(hypotheses []models.Hypothesis, max int) [][]models.Hypothesis {
	expanded := make([]models.Hypothesis, 0, len(hypotheses))
	for _, hypothesis := range hypotheses {
		for _, part := range textbudget.SplitText(hypothesis.Content, max) {
			copyHypothesis := hypothesis
			copyHypothesis.Content = part
			expanded = append(expanded, copyHypothesis)
		}
	}
	return chunkItems(expanded, max, hypothesisSize)
}

func chunkItems[T any](items []T, max int, size func(T) int) [][]T {
	if len(items) == 0 {
		return nil
	}
	if max <= 0 {
		return [][]T{append([]T(nil), items...)}
	}
	result := make([][]T, 0, (len(items)+1)/2)
	current := make([]T, 0)
	used := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		result = append(result, current)
		current = make([]T, 0)
		used = 0
	}
	for _, item := range items {
		itemSize := size(item)
		separator := 0
		if len(current) > 0 {
			separator = 1
		}
		if len(current) > 0 && used+separator+itemSize > max {
			flush()
		}
		if len(current) == 0 {
			current = append(current, item)
			used = itemSize
			continue
		}
		current = append(current, item)
		used += 1 + itemSize
	}
	flush()
	return result
}

func structuredFactText(result *StructuredFact) string {
	parts := make([]string, 0, 4)
	if result.Entity != "" {
		parts = append(parts, "Entity: "+result.Entity)
	}
	if result.Property != "" {
		parts = append(parts, "Property: "+result.Property)
	}
	if result.Value != "" {
		parts = append(parts, "Value: "+result.Value)
	}
	return strings.Join(parts, "; ")
}

// splitWorkPlanInHalf is used only after a provider rejected an apparently
// fitting request. It keeps component IDs and decisions intact while making
// the next request deterministic.
func splitWorkPlanInHalf(plan models.WorkPlan) []models.WorkPlan {
	if len(plan.Components) < 2 {
		return []models.WorkPlan{plan}
	}
	mid := len(plan.Components) / 2
	return []models.WorkPlan{
		{Components: append([]models.WorkPlanComponent(nil), plan.Components[:mid]...), Decisions: plan.Decisions},
		{Components: append([]models.WorkPlanComponent(nil), plan.Components[mid:]...), Decisions: plan.Decisions},
	}
}

func (l *Limited) splitWorkPlans(plan models.WorkPlan) []models.WorkPlan {
	if l.maxInputBytes <= 0 {
		return []models.WorkPlan{plan}
	}
	var result []models.WorkPlan
	current := models.WorkPlan{Components: make([]models.WorkPlanComponent, 0)}
	for _, component := range plan.Components {
		for _, componentPart := range splitOversizedComponent(component, l.maxInputBytes) {
			candidate := current
			candidate.Components = append(append([]models.WorkPlanComponent(nil), current.Components...), componentPart)
			candidate.Decisions = decisionsForComponents(plan.Decisions, candidate.Components)
			payload, _ := json.Marshal(candidate)
			if len(current.Components) > 0 && len(payload) > l.maxInputBytes {
				result = append(result, current)
				current = models.WorkPlan{Components: []models.WorkPlanComponent{componentPart}}
				current.Decisions = decisionsForComponents(plan.Decisions, current.Components)
				continue
			}
			current = candidate
		}
	}
	if len(current.Components) > 0 {
		result = append(result, current)
	}
	if len(result) == 0 {
		return []models.WorkPlan{plan}
	}
	return result
}

func splitOversizedComponent(component models.WorkPlanComponent, max int) []models.WorkPlanComponent {
	if max <= 0 {
		return []models.WorkPlanComponent{component}
	}
	payload, _ := json.Marshal(models.WorkPlan{Components: []models.WorkPlanComponent{component}})
	if len(payload) <= max || len(component.Tasks) < 2 {
		return []models.WorkPlanComponent{component}
	}
	mid := len(component.Tasks) / 2
	left := component
	left.Tasks = append([]models.WorkPlanTask(nil), component.Tasks[:mid]...)
	right := component
	right.Tasks = append([]models.WorkPlanTask(nil), component.Tasks[mid:]...)
	return []models.WorkPlanComponent{left, right}
}

func decisionsForComponents(decisions []models.WorkPlanDecision, components []models.WorkPlanComponent) []models.WorkPlanDecision {
	componentIDs := make(map[int64]struct{}, len(components))
	workItemIDs := make(map[int64]struct{})
	for _, component := range components {
		componentIDs[component.ID] = struct{}{}
		workItemIDs[component.ID] = struct{}{}
		for _, task := range component.Tasks {
			workItemIDs[task.ID] = struct{}{}
		}
	}
	result := make([]models.WorkPlanDecision, 0, len(decisions))
	for _, decision := range decisions {
		if decision.ComponentID != nil {
			if _, ok := componentIDs[*decision.ComponentID]; !ok {
				continue
			}
		} else if decision.WorkItemID != nil {
			if _, ok := workItemIDs[*decision.WorkItemID]; !ok {
				continue
			}
		}
		result = append(result, decision)
	}
	return result
}
