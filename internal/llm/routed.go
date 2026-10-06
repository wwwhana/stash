package llm

import (
	"context"

	"github.com/alash3al/stash/internal/embedder"
	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/reasoner"
)

// routedEmbedder forwards to whichever client the embedding feature resolves
// to at call time. Brain holds this one value for the life of the process.
type routedEmbedder struct{ router *Router }

func (e routedEmbedder) current() (*route, error) {
	rt := e.router.route(FeatureEmbedding)
	if rt == nil || rt.embedder == nil {
		return rt, unavailable(embedder.ErrUnavailable, FeatureEmbedding, rt)
	}
	return rt, nil
}

func (e routedEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	rt, err := e.current()
	if err != nil {
		return nil, err
	}
	return rt.embedder.Embed(ctx, text)
}

func (e routedEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	rt, err := e.current()
	if err != nil {
		return nil, err
	}
	return rt.embedder.EmbedQuery(ctx, text)
}

// Available lets the retry worker skip a pass, without claiming rows, while
// no embedding provider is configured.
func (e routedEmbedder) Available() bool {
	rt := e.router.route(FeatureEmbedding)
	return rt != nil && rt.embedder != nil
}

// Model reports the effective model even while the client is unavailable so
// pending rows are tagged with the model they will be indexed with.
func (e routedEmbedder) Model() string {
	if rt := e.router.route(FeatureEmbedding); rt != nil {
		return rt.Model
	}
	return ""
}

func (e routedEmbedder) Dims() int {
	if rt := e.router.route(FeatureEmbedding); rt != nil {
		return rt.Dimensions
	}
	return 0
}

// routedReasoner sends consolidation stages to the consolidation route and
// work-plan validation to its own route, so one process can use a cheap
// model for extraction and a stronger one for reviews.
type routedReasoner struct{ router *Router }

func (r routedReasoner) for_(feature Feature) (reasoner.Reasoner, error) {
	return r.router.ReasonerFor(feature)
}

func (r routedReasoner) ReasonStructured(ctx context.Context, texts []string) (*reasoner.StructuredFact, error) {
	inner, err := r.for_(FeatureConsolidation)
	if err != nil {
		return nil, err
	}
	return inner.ReasonStructured(ctx, texts)
}

func (r routedReasoner) ReasonRelationships(ctx context.Context, factContent string) ([]*reasoner.StructuredRelationship, error) {
	inner, err := r.for_(FeatureConsolidation)
	if err != nil {
		return nil, err
	}
	return inner.ReasonRelationships(ctx, factContent)
}

func (r routedReasoner) ReasonPatterns(ctx context.Context, facts []models.Fact, relationships []models.Relationship) ([]*reasoner.StructuredPattern, error) {
	inner, err := r.for_(FeatureConsolidation)
	if err != nil {
		return nil, err
	}
	return inner.ReasonPatterns(ctx, facts, relationships)
}

func (r routedReasoner) ReasonContradiction(ctx context.Context, entity, property, oldValue, newValue string) (*reasoner.ContradictionResult, error) {
	inner, err := r.for_(FeatureConsolidation)
	if err != nil {
		return nil, err
	}
	return inner.ReasonContradiction(ctx, entity, property, oldValue, newValue)
}

func (r routedReasoner) ReasonCausalLinks(ctx context.Context, facts []models.Fact) ([]*reasoner.StructuredCausalLink, error) {
	inner, err := r.for_(FeatureConsolidation)
	if err != nil {
		return nil, err
	}
	return inner.ReasonCausalLinks(ctx, facts)
}

func (r routedReasoner) ReasonGoalProgress(ctx context.Context, goals []models.Goal, facts []models.Fact) ([]*reasoner.GoalProgressAssessment, error) {
	inner, err := r.for_(FeatureConsolidation)
	if err != nil {
		return nil, err
	}
	return inner.ReasonGoalProgress(ctx, goals, facts)
}

func (r routedReasoner) ReasonFailurePatterns(ctx context.Context, failures []models.Failure, evidence []string) ([]*reasoner.FailurePatternResult, error) {
	inner, err := r.for_(FeatureConsolidation)
	if err != nil {
		return nil, err
	}
	return inner.ReasonFailurePatterns(ctx, failures, evidence)
}

func (r routedReasoner) ReasonHypothesisEvidence(ctx context.Context, hypotheses []models.Hypothesis, facts []models.Fact) ([]*reasoner.HypothesisEvidenceResult, error) {
	inner, err := r.for_(FeatureConsolidation)
	if err != nil {
		return nil, err
	}
	return inner.ReasonHypothesisEvidence(ctx, hypotheses, facts)
}

// ValidateWorkPlan implements reasoner.WorkPlanValidator over the
// plan_validation route.
func (r routedReasoner) ValidateWorkPlan(ctx context.Context, plan models.WorkPlan) (*reasoner.WorkPlanValidationResult, error) {
	inner, err := r.for_(FeaturePlanValidation)
	if err != nil {
		return nil, err
	}
	validator, ok := inner.(reasoner.WorkPlanValidator)
	if !ok {
		return nil, unavailable(reasoner.ErrUnavailable, FeaturePlanValidation, nil)
	}
	return validator.ValidateWorkPlan(ctx, plan)
}

// ModelName reports the plan-validation model, which is what gets recorded
// with a stored validation result.
func (r routedReasoner) ModelName() string {
	if rt := r.router.route(FeaturePlanValidation); rt != nil {
		return rt.Model
	}
	return ""
}

// DraftWikiPage implements reasoner.WikiAuthor over the wiki route.
func (r routedReasoner) DraftWikiPage(ctx context.Context, request reasoner.WikiDraftRequest) (*reasoner.WikiDraft, error) {
	inner, err := r.for_(FeatureWiki)
	if err != nil {
		return nil, err
	}
	author, ok := inner.(reasoner.WikiAuthor)
	if !ok {
		return nil, unavailable(reasoner.ErrUnavailable, FeatureWiki, nil)
	}
	return author.DraftWikiPage(ctx, request)
}
