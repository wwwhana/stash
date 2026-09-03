package brain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/reasoner"
)

type contradictionTestReasoner struct {
	result *reasoner.ContradictionResult
	err    error
}

func (r contradictionTestReasoner) ReasonStructured(context.Context, []string) (*reasoner.StructuredFact, error) {
	return nil, nil
}

func (r contradictionTestReasoner) ReasonRelationships(context.Context, string) ([]*reasoner.StructuredRelationship, error) {
	return nil, nil
}

func (r contradictionTestReasoner) ReasonPatterns(context.Context, []models.Fact, []models.Relationship) ([]*reasoner.StructuredPattern, error) {
	return nil, nil
}

func (r contradictionTestReasoner) ReasonContradiction(context.Context, string, string, string, string) (*reasoner.ContradictionResult, error) {
	return r.result, r.err
}

func (r contradictionTestReasoner) ReasonCausalLinks(context.Context, []models.Fact) ([]*reasoner.StructuredCausalLink, error) {
	return nil, nil
}

func (r contradictionTestReasoner) ReasonGoalProgress(context.Context, []models.Goal, []models.Fact) ([]*reasoner.GoalProgressAssessment, error) {
	return nil, nil
}

func (r contradictionTestReasoner) ReasonFailurePatterns(context.Context, []models.Failure, []string) ([]*reasoner.FailurePatternResult, error) {
	return nil, nil
}

func (r contradictionTestReasoner) ReasonHypothesisEvidence(context.Context, []models.Hypothesis, []models.Fact) ([]*reasoner.HypothesisEvidenceResult, error) {
	return nil, nil
}

func TestDetectContradictionsSupersedesCorrectionPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	oldFact := insertContradictionTestFact(t, b, ctx, namespaceID, "API port is 8080", "8080")
	newFact := insertContradictionTestFact(t, b, ctx, namespaceID, "API port is now 9090", "9090")
	b.reasoner = contradictionTestReasoner{result: &reasoner.ContradictionResult{
		Classification: reasoner.ClassificationReplacement,
		Confidence:     0.95,
	}}

	detected, autoResolved, err := b.DetectContradictions(ctx, namespaceID, newFact)
	if err != nil {
		t.Fatalf("DetectContradictions: %v", err)
	}
	if detected != 1 || autoResolved != 1 {
		t.Fatalf("detected, auto-resolved = %d, %d; want 1, 1", detected, autoResolved)
	}
	var superseded bool
	if err := b.pool.QueryRow(ctx, `SELECT valid_until IS NOT NULL FROM facts WHERE id = $1`, oldFact.ID).Scan(&superseded); err != nil {
		t.Fatalf("read superseded fact: %v", err)
	}
	if !superseded {
		t.Fatal("old fact remains active")
	}
	assertWorkExecutionRowCount(t, b, ctx, 1,
		`SELECT count(*) FROM contradictions WHERE old_fact_id = $1 AND new_fact_id = $2 AND resolved AND resolution = 'superseded'`,
		oldFact.ID, newFact.ID,
	)
}

func TestDetectContradictionsReturnsReasonerErrorPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	insertContradictionTestFact(t, b, ctx, namespaceID, "API port is 8080", "8080")
	newFact := insertContradictionTestFact(t, b, ctx, namespaceID, "API port is now 9090", "9090")
	b.reasoner = contradictionTestReasoner{err: errors.New("provider unavailable")}

	_, _, err := b.DetectContradictions(ctx, namespaceID, newFact)
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("DetectContradictions error = %v", err)
	}
}

func TestRecordContradictionIsIdempotentPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	oldFact := insertContradictionTestFact(t, b, ctx, namespaceID, "API port is 8080", "8080")
	newFact := insertContradictionTestFact(t, b, ctx, namespaceID, "API port may be 9090", "9090")
	b.reasoner = contradictionTestReasoner{result: &reasoner.ContradictionResult{
		Classification: reasoner.ClassificationContradiction,
		Confidence:     0.8,
	}}

	for attempt := 0; attempt < 2; attempt++ {
		if _, _, err := b.DetectContradictions(ctx, namespaceID, newFact); err != nil {
			t.Fatalf("DetectContradictions attempt %d: %v", attempt+1, err)
		}
	}
	assertWorkExecutionRowCount(t, b, ctx, 1,
		`SELECT count(*) FROM contradictions WHERE old_fact_id = $1 AND new_fact_id = $2`,
		oldFact.ID, newFact.ID,
	)
	b.reasoner = contradictionTestReasoner{result: &reasoner.ContradictionResult{
		Classification: reasoner.ClassificationReplacement,
		Confidence:     0.95,
	}}
	if _, autoResolved, err := b.DetectContradictions(ctx, namespaceID, newFact); err != nil || autoResolved != 1 {
		t.Fatalf("replacement after contradiction = %d, %v; want 1, nil", autoResolved, err)
	}
	assertWorkExecutionRowCount(t, b, ctx, 1,
		`SELECT count(*) FROM contradictions WHERE old_fact_id = $1 AND new_fact_id = $2 AND resolved AND resolution = 'superseded'`,
		oldFact.ID, newFact.ID,
	)
}

func insertContradictionTestFact(t *testing.T, b *Brain, ctx context.Context, namespaceID int64, content, value string) *models.Fact {
	t.Helper()
	var id int64
	if err := b.pool.QueryRow(ctx,
		`INSERT INTO facts (namespace_id, content, confidence, entity, property, value, valid_from)
		 VALUES ($1, $2, 0.9, 'API', 'port', $3, $4) RETURNING id`,
		namespaceID, content, value, time.Now().UTC(),
	).Scan(&id); err != nil {
		t.Fatalf("insert contradiction test fact: %v", err)
	}
	entity, property := "API", "port"
	return &models.Fact{
		ID: id, NamespaceID: namespaceID, Content: content, Confidence: 0.9,
		Entity: &entity, Property: &property, Value: &value,
	}
}
