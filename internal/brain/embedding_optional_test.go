package brain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/embedder"
	"github.com/alash3al/stash/internal/queries"
	"github.com/alash3al/stash/internal/reasoner"
)

// unavailableEmbedder behaves like the router when no embedding provider is
// assigned: every call fails fast and the worker is told to stand by.
type unavailableEmbedder struct{}

func (unavailableEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, embedder.ErrUnavailable
}
func (unavailableEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, embedder.ErrUnavailable
}
func (unavailableEmbedder) Model() string   { return "" }
func (unavailableEmbedder) Dims() int       { return 0 }
func (unavailableEmbedder) Available() bool { return false }

// textFactReasoner extracts one fixed fact so stage 1 has something to store.
type textFactReasoner struct{ contradictionTestReasoner }

func (textFactReasoner) ReasonStructured(context.Context, []string) (*reasoner.StructuredFact, error) {
	return &reasoner.StructuredFact{Entity: "api", Property: "port", Value: "8080", Summary: "API listens on port 8080"}, nil
}

func TestMemoryWorksWithoutEmbeddingProviderPostgres(t *testing.T) {
	b, ctx, namespaceID := newWorkExecutionTestBrain(t)
	q, err := queries.New()
	if err != nil {
		t.Fatalf("queries: %v", err)
	}
	b.queries = q
	b.embedder = unavailableEmbedder{}
	b.reasoner = textFactReasoner{}
	var slug string
	if err := b.pool.QueryRow(ctx, `SELECT slug FROM namespaces WHERE id = $1`, namespaceID).Scan(&slug); err != nil {
		t.Fatalf("read namespace slug: %v", err)
	}

	remembered, err := b.RememberWithStatus(ctx, slug, "FROMM-4414 deploy failed on staging because the migration timed out", nil)
	if err != nil {
		t.Fatalf("RememberWithStatus: %v", err)
	}
	if remembered.Indexed || !remembered.EmbeddingUnavailable || remembered.RetryAt == nil {
		t.Fatalf("remember result = %+v, want pending and unavailable", remembered)
	}
	var attempts int
	var retryAt time.Time
	if err := b.pool.QueryRow(ctx, `SELECT embedding_attempts, embedding_retry_at FROM episodes WHERE id = $1`, remembered.ID).Scan(&attempts, &retryAt); err != nil {
		t.Fatalf("read episode: %v", err)
	}
	if attempts != 0 || retryAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("episode attempts=%d retry_at=%s, want due now without a counted attempt", attempts, retryAt)
	}

	// The worker must not burn attempts or pause rows while nothing can run.
	pass, err := b.RetryPendingEmbeddings(ctx, 10)
	if err != nil {
		t.Fatalf("RetryPendingEmbeddings: %v", err)
	}
	if pass.Attempted != 0 || pass.Pending < 1 {
		t.Fatalf("retry pass = %+v, want nothing attempted and pending rows reported", pass)
	}
	status, err := b.EmbeddingMaintenanceStatus(ctx)
	if err != nil || status.ProviderAvailable {
		t.Fatalf("maintenance status = %+v, %v; want provider unavailable", status, err)
	}

	// Keyword search and forget both work on the unindexed memory.
	results, err := b.RecallWithOptions(ctx, []string{slug}, "FROMM-4414", 5, RecallOptions{})
	if err != nil {
		t.Fatalf("RecallWithOptions: %v", err)
	}
	found := false
	for _, r := range results {
		if r.Type == "episode" && r.ID == remembered.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("recall results %+v do not contain episode %d", results, remembered.ID)
	}
	match, err := b.ForgetEpisodeMatch(ctx, []string{slug}, "FROMM-4414 deploy failed", ForgetOptions{DryRun: true})
	if err != nil || match.ID != remembered.ID || match.Deleted {
		t.Fatalf("ForgetEpisodeMatch = %+v, %v; want dry-run match of %d", match, err, remembered.ID)
	}

	// Consolidation still produces facts; they wait for a vector.
	result, err := b.ConsolidateByID(ctx, namespaceID)
	if err != nil {
		t.Fatalf("ConsolidateByID: %v", err)
	}
	if result.FactsCreated != 1 {
		t.Fatalf("consolidation = %+v, want one fact created", result)
	}
	var factID int64
	var factAttempts int
	var hasVector bool
	if err := b.pool.QueryRow(ctx,
		`SELECT id, embedding_attempts, embedding IS NOT NULL FROM facts WHERE namespace_id = $1 AND deleted_at IS NULL ORDER BY id DESC LIMIT 1`,
		namespaceID).Scan(&factID, &factAttempts, &hasVector); err != nil {
		t.Fatalf("read fact: %v", err)
	}
	if hasVector || factAttempts != 0 {
		t.Fatalf("fact %d has vector=%v attempts=%d, want pending without attempts", factID, hasVector, factAttempts)
	}

	// A restated fact is caught by the text check instead of being duplicated.
	if _, err := b.RememberWithStatus(ctx, slug, "Confirmed again: the API port is 8080", nil); err != nil {
		t.Fatalf("remember second episode: %v", err)
	}
	result, err = b.ConsolidateByID(ctx, namespaceID)
	if err != nil {
		t.Fatalf("second ConsolidateByID: %v", err)
	}
	if result.FactsCreated != 0 || result.FactsDeduplicated != 1 {
		t.Fatalf("second consolidation = %+v, want the restatement deduplicated", result)
	}

	// Confirming a hypothesis is a user decision and must not need a vector.
	hypothesis, err := b.CreateHypothesis(ctx, namespaceID, "Staging shares the production database", "compare DSNs", 0.5, nil)
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}
	if _, err := b.UpdateHypothesisStatus(ctx, hypothesis.ID, "testing"); err != nil {
		t.Fatalf("UpdateHypothesisStatus: %v", err)
	}
	_, fact, err := b.ConfirmHypothesis(ctx, hypothesis.ID)
	if err != nil || fact == nil {
		t.Fatalf("ConfirmHypothesis = %v, %v", fact, err)
	}

	if _, err := b.Reindex(ctx, false, nil); !errors.Is(err, embedder.ErrUnavailable) {
		t.Fatalf("Reindex without a provider error = %v, want ErrUnavailable", err)
	}
}
