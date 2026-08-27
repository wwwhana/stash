package reasoner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alash3al/stash/internal/models"
)

type limitedTestReasoner struct {
	maxBytes       int
	structuredCall [][]string
}

func (r *limitedTestReasoner) ReasonStructured(_ context.Context, texts []string) (*StructuredFact, error) {
	r.structuredCall = append(r.structuredCall, append([]string(nil), texts...))
	used := 0
	for _, text := range texts {
		used += len(text)
	}
	if r.maxBytes > 0 && used > r.maxBytes {
		return nil, errors.New("message too long: context window exceeded")
	}
	return &StructuredFact{Summary: strings.Join(texts, " ")}, nil
}

func (r *limitedTestReasoner) ReasonRelationships(context.Context, string) ([]*StructuredRelationship, error) {
	return nil, nil
}

func (r *limitedTestReasoner) ReasonPatterns(context.Context, []models.Fact, []models.Relationship) ([]*StructuredPattern, error) {
	return nil, nil
}

func (r *limitedTestReasoner) ReasonContradiction(context.Context, string, string, string, string) (*ContradictionResult, error) {
	return nil, nil
}

func (r *limitedTestReasoner) ReasonCausalLinks(context.Context, []models.Fact) ([]*StructuredCausalLink, error) {
	return nil, nil
}

func (r *limitedTestReasoner) ReasonGoalProgress(context.Context, []models.Goal, []models.Fact) ([]*GoalProgressAssessment, error) {
	return nil, nil
}

func (r *limitedTestReasoner) ReasonFailurePatterns(context.Context, []models.Failure, []string) ([]*FailurePatternResult, error) {
	return nil, nil
}

func (r *limitedTestReasoner) ReasonHypothesisEvidence(context.Context, []models.Hypothesis, []models.Fact) ([]*HypothesisEvidenceResult, error) {
	return nil, nil
}

func (r *limitedTestReasoner) ModelName() string { return "limited-test-model" }

func (r *limitedTestReasoner) ValidateWorkPlan(context.Context, models.WorkPlan) (*WorkPlanValidationResult, error) {
	return &WorkPlanValidationResult{Summary: "ok"}, nil
}

func TestLimitedReasonerSplitsStructuredInputByConfiguredModelBudget(t *testing.T) {
	inner := &limitedTestReasoner{}
	limited := NewLimited(inner, 40, 0)
	texts := []string{
		"첫 문단의 첫 문장입니다. 둘째 문장입니다.",
		"두 번째 문단의 첫 문장입니다.",
		"세 번째 문단입니다.",
	}
	result, err := limited.ReasonStructured(context.Background(), texts)
	if err != nil {
		t.Fatalf("ReasonStructured: %v", err)
	}
	if result == nil || result.Summary == "" {
		t.Fatalf("result = %#v, want synthesized summary", result)
	}
	if len(inner.structuredCall) < 2 {
		t.Fatalf("underlying calls = %d, want chunked calls", len(inner.structuredCall))
	}
}

func TestLimitedReasonerUsesModelWindowMinusReserve(t *testing.T) {
	limited := NewLimited(&limitedTestReasoner{}, 44544, 4096)
	if got, want := limited.MaxInputBytes(), 40448; got != want {
		t.Fatalf("MaxInputBytes = %d, want %d", got, want)
	}
}

func TestLimitedReasonerAdaptsWhenContextWindowIsUnknown(t *testing.T) {
	inner := &limitedTestReasoner{maxBytes: 24}
	limited := NewLimited(inner, 0, 0)
	texts := []string{strings.Repeat("문장. ", 20)}
	if _, err := limited.ReasonStructured(context.Background(), texts); err != nil {
		t.Fatalf("adaptive ReasonStructured: %v", err)
	}
	if len(inner.structuredCall) < 2 {
		t.Fatalf("underlying calls = %d, want adaptive retries", len(inner.structuredCall))
	}
}
