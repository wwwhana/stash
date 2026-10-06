package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/db"
	"github.com/alash3al/stash/internal/embedder"
	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/reasoner"
)

type fakeEmbedder struct{ spec clientSpec }

func (f *fakeEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}
func (f *fakeEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return []float32{1}, nil
}
func (f *fakeEmbedder) Model() string { return f.spec.model }
func (f *fakeEmbedder) Dims() int     { return f.spec.dims }

type fakeReasoner struct {
	reasoner.Reasoner
	spec clientSpec
}

func (f *fakeReasoner) ReasonStructured(context.Context, []string) (*reasoner.StructuredFact, error) {
	return &reasoner.StructuredFact{Summary: "from " + f.spec.model}, nil
}

func (f *fakeReasoner) ValidateWorkPlan(context.Context, models.WorkPlan) (*reasoner.WorkPlanValidationResult, error) {
	return &reasoner.WorkPlanValidationResult{Summary: "validated by " + f.spec.model}, nil
}

func (f *fakeReasoner) ModelName() string { return f.spec.model }

type fakeFactory struct {
	built     []clientSpec
	failModel string
}

func (f *fakeFactory) newEmbedder(spec clientSpec) (embedder.Embedder, error) {
	f.built = append(f.built, spec)
	if spec.model == f.failModel {
		return nil, errors.New("boom")
	}
	return &fakeEmbedder{spec: spec}, nil
}

func (f *fakeFactory) newReasoner(spec clientSpec) (reasoner.Reasoner, error) {
	f.built = append(f.built, spec)
	if spec.model == f.failModel {
		return nil, errors.New("boom")
	}
	return &fakeReasoner{spec: spec}, nil
}

func testEnv() *EnvProvider {
	return &EnvProvider{
		BaseURL: "http://env.local/v1", APIKey: "env-key", RequestTimeout: time.Minute,
		EmbeddingModel: "env-embed", VectorDim: 8, ReasonerModel: "env-reason", ReasonerReservedTokens: 4096,
	}
}

func TestRouterServesEnvironmentWhenNothingIsStored(t *testing.T) {
	factory := &fakeFactory{}
	prepared := []string{}
	router := NewRouter(nil, nil, testEnv(), Options{factory: factory, PrepareStorage: func(_ context.Context, model string, dims int) (db.EmbeddingStorageReport, error) {
		prepared = append(prepared, model)
		return db.EmbeddingStorageReport{}, nil
	}})
	if err := router.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	for _, rt := range router.Status() {
		if !rt.Available || rt.Source != SourceEnvironment || rt.ProviderName != EnvProviderName {
			t.Fatalf("route %s = %+v, want available environment route", rt.Feature, rt)
		}
	}
	if router.Embedder().Model() != "env-embed" || router.Embedder().Dims() != 8 {
		t.Fatalf("embedder reports %s/%d", router.Embedder().Model(), router.Embedder().Dims())
	}
	if len(prepared) != 1 || prepared[0] != "env-embed" {
		t.Fatalf("storage prepared for %v, want once for env-embed", prepared)
	}
	// An unchanged route must keep its client and not prepare storage again.
	built := len(factory.built)
	if err := router.Reload(context.Background()); err != nil {
		t.Fatalf("second Reload: %v", err)
	}
	if len(factory.built) != built || len(prepared) != 1 {
		t.Fatalf("unchanged reload rebuilt clients (%d→%d) or storage (%d)", built, len(factory.built), len(prepared))
	}
}

func TestRouterReportsUnavailableFeaturesWithoutFailingCallers(t *testing.T) {
	router := NewRouter(nil, nil, nil, Options{factory: &fakeFactory{}})
	if err := router.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if _, err := router.Embedder().Embed(context.Background(), "x"); !errors.Is(err, embedder.ErrUnavailable) {
		t.Fatalf("Embed error = %v, want ErrUnavailable", err)
	}
	if probe, ok := router.Embedder().(interface{ Available() bool }); !ok || probe.Available() {
		t.Fatal("embedder should report itself unavailable")
	}
	if _, err := router.Reasoner().ReasonStructured(context.Background(), nil); !errors.Is(err, reasoner.ErrUnavailable) {
		t.Fatalf("ReasonStructured error = %v, want ErrUnavailable", err)
	}
	validator := router.Reasoner().(reasoner.WorkPlanValidator)
	if _, err := validator.ValidateWorkPlan(context.Background(), models.WorkPlan{}); !errors.Is(err, reasoner.ErrUnavailable) {
		t.Fatalf("ValidateWorkPlan error = %v, want ErrUnavailable", err)
	}
	for _, rt := range router.Status() {
		if rt.Available || rt.Source != SourceNone || !strings.Contains(rt.Error, "no provider is assigned") {
			t.Fatalf("route %s = %+v, want unavailable none route", rt.Feature, rt)
		}
	}
}

func TestRouterKeepsOtherFeaturesWhenOneClientFails(t *testing.T) {
	factory := &fakeFactory{failModel: "env-embed"}
	router := NewRouter(nil, nil, testEnv(), Options{factory: factory})
	if err := router.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	status := map[Feature]RouteStatus{}
	for _, rt := range router.Status() {
		status[rt.Feature] = rt
	}
	if status[FeatureEmbedding].Available || status[FeatureEmbedding].Error != "boom" {
		t.Fatalf("embedding route = %+v, want build failure recorded", status[FeatureEmbedding])
	}
	if !status[FeatureConsolidation].Available || !status[FeaturePlanValidation].Available {
		t.Fatalf("reasoning routes should still be available: %+v", status)
	}
	fact, err := router.Reasoner().ReasonStructured(context.Background(), nil)
	if err != nil || fact.Summary != "from env-reason" {
		t.Fatalf("ReasonStructured = %+v, %v", fact, err)
	}
}

func TestRoutedReasonerSendsPlanValidationToItsOwnRoute(t *testing.T) {
	router := NewRouter(nil, nil, testEnv(), Options{factory: &fakeFactory{}})
	if err := router.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	// Point plan validation at a different model by editing the snapshot the
	// way a database assignment would resolve it.
	snap := router.current.Load()
	snap.routes[FeaturePlanValidation].reasoner = &fakeReasoner{spec: clientSpec{model: "review-model"}}
	snap.routes[FeaturePlanValidation].Model = "review-model"

	validator := router.Reasoner().(reasoner.WorkPlanValidator)
	result, err := validator.ValidateWorkPlan(context.Background(), models.WorkPlan{})
	if err != nil || result.Summary != "validated by review-model" {
		t.Fatalf("ValidateWorkPlan = %+v, %v", result, err)
	}
	if validator.ModelName() != "review-model" {
		t.Fatalf("ModelName = %q", validator.ModelName())
	}
	fact, _ := router.Reasoner().ReasonStructured(context.Background(), nil)
	if fact.Summary != "from env-reason" {
		t.Fatalf("consolidation still routes to env model, got %q", fact.Summary)
	}
}

func TestRouterDisablesEmbeddingWhenStoragePreparationFails(t *testing.T) {
	router := NewRouter(nil, nil, testEnv(), Options{factory: &fakeFactory{}, PrepareStorage: func(context.Context, string, int) (db.EmbeddingStorageReport, error) {
		return db.EmbeddingStorageReport{}, errors.New("dimension too large")
	}})
	if err := router.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	var embeddingRoute RouteStatus
	for _, rt := range router.Status() {
		if rt.Feature == FeatureEmbedding {
			embeddingRoute = rt
		}
	}
	if embeddingRoute.Available || !strings.Contains(embeddingRoute.Error, "dimension too large") {
		t.Fatalf("embedding route = %+v", embeddingRoute)
	}
	if _, err := router.Embedder().Embed(context.Background(), "x"); !errors.Is(err, embedder.ErrUnavailable) {
		t.Fatalf("Embed error = %v", err)
	}
}
