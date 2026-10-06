package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alash3al/stash/internal/db"
	"github.com/alash3al/stash/internal/embedder"
	"github.com/alash3al/stash/internal/reasoner"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvProvider is the STASH_OPENAI_* configuration. It serves any feature
// without a database assignment so existing deployments keep working.
type EnvProvider struct {
	BaseURL                string
	APIKey                 string
	RequestTimeout         time.Duration
	EmbeddingModel         string
	VectorDim              int
	EmbeddingContextTokens int
	ReasonerModel          string
	ReasonerContextTokens  int
	ReasonerReservedTokens int
}

// EnvProviderName is the reserved provider name used when the environment
// configuration is imported into the database.
const EnvProviderName = "environment"

func (e *EnvProvider) serves(kind Kind) bool {
	if e == nil || strings.TrimSpace(e.BaseURL) == "" {
		return false
	}
	switch kind {
	case KindEmbedding:
		return strings.TrimSpace(e.EmbeddingModel) != "" && e.VectorDim > 0
	case KindReasoning:
		return strings.TrimSpace(e.ReasonerModel) != ""
	}
	return false
}

// RouteStatus is the operator-visible resolution of one feature.
type RouteStatus struct {
	Feature        Feature `json:"feature"`
	Kind           Kind    `json:"kind"`
	Source         string  `json:"source"`
	ProviderID     int64   `json:"provider_id,omitempty"`
	ProviderName   string  `json:"provider_name,omitempty"`
	Model          string  `json:"model,omitempty"`
	Dimensions     int     `json:"dimensions,omitempty"`
	ContextTokens  int     `json:"context_tokens,omitempty"`
	ReservedTokens int     `json:"reserved_tokens,omitempty"`
	Available      bool    `json:"available"`
	Error          string  `json:"error,omitempty"`
}

const (
	SourceDatabase    = "database"
	SourceEnvironment = "environment"
	SourceNone        = "none"
)

// Options tunes how the router builds clients.
type Options struct {
	Logger *slog.Logger
	// EmbeddingCache stores vectors in embedding_cache so repeated text does
	// not hit the provider twice.
	EmbeddingCache bool
	// PrepareStorage aligns the pgvector columns with the effective embedding
	// model and dimension. nil skips it, which only tests should do.
	PrepareStorage func(ctx context.Context, model string, dims int) (db.EmbeddingStorageReport, error)

	// factory lets tests substitute fake clients.
	factory clientFactory
}

type clientSpec struct {
	baseURL        string
	apiKey         string
	model          string
	dims           int
	contextTokens  int
	reservedTokens int
	timeout        time.Duration
}

// signature identifies a built client so an unchanged route keeps its
// client, and with it the in-flight request de-duplication, across reloads.
func (s clientSpec) signature(kind Kind) string {
	keyHash := sha256.Sum256([]byte(s.apiKey))
	return fmt.Sprintf("%s|%s|%s|%s|%d|%d|%d|%s", kind, s.baseURL, hex.EncodeToString(keyHash[:8]), s.model, s.dims, s.contextTokens, s.reservedTokens, s.timeout)
}

type clientFactory interface {
	newEmbedder(spec clientSpec) (embedder.Embedder, error)
	newReasoner(spec clientSpec) (reasoner.Reasoner, error)
}

type openAIFactory struct {
	logger *slog.Logger
	pool   *pgxpool.Pool
	cache  bool
}

func (f openAIFactory) newEmbedder(spec clientSpec) (embedder.Embedder, error) {
	client, err := embedder.NewOpenAIWithTimeoutAndLogger(spec.baseURL, spec.apiKey, spec.model, spec.dims, spec.timeout, f.logger)
	if err != nil {
		return nil, err
	}
	// Split oversized passages before the cache/API boundary. The cache still
	// stores the final vector under the original full memory text.
	var built embedder.Embedder = embedder.NewLimited(client, spec.contextTokens)
	if f.cache && f.pool != nil {
		built = embedder.NewCached(built, f.pool)
	}
	return built, nil
}

func (f openAIFactory) newReasoner(spec clientSpec) (reasoner.Reasoner, error) {
	client, err := reasoner.NewOpenAIWithTimeoutAndLogger(spec.baseURL, spec.apiKey, spec.model, spec.timeout, f.logger)
	if err != nil {
		return nil, err
	}
	// Keep model-sized batching at the reasoner boundary so every consolidation
	// stage uses the same context rules.
	return reasoner.NewLimited(client, spec.contextTokens, spec.reservedTokens), nil
}

type route struct {
	RouteStatus
	signature string
	embedder  embedder.Embedder
	reasoner  reasoner.Reasoner
}

type snapshot struct {
	version  int64
	loadedAt time.Time
	routes   map[Feature]*route
}

// Router resolves features to live clients and swaps them atomically when
// the stored configuration changes.
type Router struct {
	store   *Store
	env     *EnvProvider
	opts    Options
	factory clientFactory

	current  atomic.Pointer[snapshot]
	reloadMu sync.Mutex
	prepared struct {
		model string
		dims  int
	}
}

// NewRouter creates a router that serves "not loaded" until Reload runs.
func NewRouter(pool *pgxpool.Pool, store *Store, env *EnvProvider, opts Options) *Router {
	factory := opts.factory
	if factory == nil {
		factory = openAIFactory{logger: opts.Logger, pool: pool, cache: opts.EmbeddingCache}
	}
	r := &Router{store: store, env: env, opts: opts, factory: factory}
	routes := map[Feature]*route{}
	for _, info := range featureCatalog {
		routes[info.Feature] = unavailableRoute(info, "model routing has not been loaded")
	}
	r.current.Store(&snapshot{routes: routes})
	return r
}

func unavailableRoute(info FeatureInfo, reason string) *route {
	return &route{RouteStatus: RouteStatus{Feature: info.Feature, Kind: info.Kind, Source: SourceNone, Error: reason}}
}

// Reload rebuilds every route from the store and environment. Build
// failures are recorded on the affected feature rather than failing the
// reload, so one bad credential cannot take the other features down.
func (r *Router) Reload(ctx context.Context) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()

	var (
		creds       map[int64]credentials
		assignments []Assignment
		version     int64
		err         error
	)
	if r.store != nil {
		if creds, err = r.store.providerCredentials(ctx); err != nil {
			return err
		}
		if assignments, err = r.store.ListAssignments(ctx); err != nil {
			return err
		}
		if version, err = r.store.Version(ctx); err != nil {
			return err
		}
	}
	assigned := map[Feature]Assignment{}
	for _, a := range assignments {
		assigned[a.Feature] = a
	}

	previous := r.current.Load()
	routes := map[Feature]*route{}
	for _, info := range featureCatalog {
		var prev *route
		if previous != nil {
			prev = previous.routes[info.Feature]
		}
		routes[info.Feature] = r.buildRoute(info, assigned, creds, prev)
	}
	r.prepareEmbeddingStorage(ctx, routes[FeatureEmbedding])

	r.current.Store(&snapshot{version: version, loadedAt: time.Now().UTC(), routes: routes})
	if r.opts.Logger != nil {
		for _, info := range featureCatalog {
			rt := routes[info.Feature]
			args := []any{"feature", rt.Feature, "source", rt.Source, "provider", rt.ProviderName, "model", rt.Model, "available", rt.Available}
			if rt.Error != "" {
				args = append(args, "error", rt.Error)
				r.opts.Logger.Warn("model route", args...)
				continue
			}
			r.opts.Logger.Info("model route", args...)
		}
	}
	return nil
}

// ReloadIfChanged reloads only when another process bumped the stored
// configuration version. It is cheap enough to run on a short ticker.
func (r *Router) ReloadIfChanged(ctx context.Context) (bool, error) {
	if r.store == nil {
		return false, nil
	}
	version, err := r.store.Version(ctx)
	if err != nil {
		return false, err
	}
	if current := r.current.Load(); current != nil && current.version == version && !current.loadedAt.IsZero() {
		return false, nil
	}
	return true, r.Reload(ctx)
}

func (r *Router) buildRoute(info FeatureInfo, assigned map[Feature]Assignment, creds map[int64]credentials, prev *route) *route {
	status := RouteStatus{Feature: info.Feature, Kind: info.Kind}
	var spec clientSpec

	if a, ok := assigned[info.Feature]; ok {
		status.Source = SourceDatabase
		status.ProviderID = a.ProviderID
		status.Model = a.Model
		status.Dimensions = a.Dimensions
		status.ContextTokens = a.ContextTokens
		status.ReservedTokens = a.ReservedTokens
		cred, found := creds[a.ProviderID]
		switch {
		case !found:
			status.Error = "assigned provider no longer exists"
		case !cred.Enabled:
			status.ProviderName = cred.Name
			status.Error = "assigned provider is disabled"
		case cred.keyError != nil:
			status.ProviderName = cred.Name
			status.Error = "stored API key cannot be opened: " + cred.keyError.Error()
		default:
			status.ProviderName = cred.Name
			reserved := a.ReservedTokens
			if info.Kind == KindReasoning && reserved == 0 {
				reserved = DefaultReservedTokens
				status.ReservedTokens = reserved
			}
			spec = clientSpec{baseURL: cred.BaseURL, apiKey: cred.apiKey, model: a.Model, dims: a.Dimensions, contextTokens: a.ContextTokens, reservedTokens: reserved, timeout: cred.Timeout()}
		}
	} else if r.env.serves(info.Kind) {
		status.Source = SourceEnvironment
		status.ProviderName = EnvProviderName
		spec = clientSpec{baseURL: r.env.BaseURL, apiKey: r.env.APIKey, timeout: r.env.RequestTimeout}
		if info.Kind == KindEmbedding {
			spec.model, spec.dims, spec.contextTokens = r.env.EmbeddingModel, r.env.VectorDim, r.env.EmbeddingContextTokens
		} else {
			spec.model, spec.contextTokens, spec.reservedTokens = r.env.ReasonerModel, r.env.ReasonerContextTokens, r.env.ReasonerReservedTokens
		}
		status.Model, status.Dimensions, status.ContextTokens, status.ReservedTokens = spec.model, spec.dims, spec.contextTokens, spec.reservedTokens
	} else {
		status.Source = SourceNone
		status.Error = "no provider is assigned and STASH_OPENAI_* is not configured for this feature"
	}

	rt := &route{RouteStatus: status}
	if status.Error != "" {
		return rt
	}
	rt.signature = spec.signature(info.Kind)
	if prev != nil && prev.signature == rt.signature && prev.Available {
		rt.embedder, rt.reasoner = prev.embedder, prev.reasoner
		rt.Available = true
		return rt
	}
	var err error
	if info.Kind == KindEmbedding {
		rt.embedder, err = r.factory.newEmbedder(spec)
	} else {
		rt.reasoner, err = r.factory.newReasoner(spec)
	}
	if err != nil {
		rt.Error = err.Error()
		return rt
	}
	rt.Available = true
	return rt
}

// prepareEmbeddingStorage resizes the vector columns and queues a reindex
// when the effective embedding model or dimension changed. Existing content
// is never touched; only vectors are recomputed by the retry worker.
func (r *Router) prepareEmbeddingStorage(ctx context.Context, rt *route) {
	if !rt.Available || r.opts.PrepareStorage == nil {
		return
	}
	if rt.Model == r.prepared.model && rt.Dimensions == r.prepared.dims {
		return
	}
	report, err := r.opts.PrepareStorage(ctx, rt.Model, rt.Dimensions)
	if err != nil {
		rt.Available = false
		rt.embedder = nil
		rt.Error = "prepare embedding storage: " + err.Error()
		return
	}
	r.prepared.model, r.prepared.dims = rt.Model, rt.Dimensions
	if r.opts.Logger != nil && (report.DimensionChanged || report.ModelChanged || report.ReindexQueued > 0) {
		r.opts.Logger.Warn("embedding reindex queued",
			"model", rt.Model,
			"dimension_changed", report.DimensionChanged,
			"model_changed", report.ModelChanged,
			"rows", report.ReindexQueued,
		)
	}
}

func (r *Router) route(feature Feature) *route {
	snap := r.current.Load()
	if snap == nil {
		return nil
	}
	return snap.routes[feature]
}

// Status returns every feature's resolution in catalog order.
func (r *Router) Status() []RouteStatus {
	out := make([]RouteStatus, 0, len(featureCatalog))
	for _, info := range featureCatalog {
		if rt := r.route(info.Feature); rt != nil {
			out = append(out, rt.RouteStatus)
		}
	}
	return out
}

// Version is the stored configuration version the current routes reflect.
func (r *Router) Version() int64 {
	if snap := r.current.Load(); snap != nil {
		return snap.version
	}
	return 0
}

// Embedder returns a stable handle that always uses the current embedding
// route. Callers keep one reference for the life of the process.
func (r *Router) Embedder() embedder.Embedder {
	return routedEmbedder{r}
}

// Reasoner returns a stable handle. Reasoning methods use the consolidation
// route; work-plan validation uses its own feature route.
func (r *Router) Reasoner() reasoner.Reasoner {
	return routedReasoner{r}
}

// ReasonerFor returns the live reasoner for a specific reasoning feature.
func (r *Router) ReasonerFor(feature Feature) (reasoner.Reasoner, error) {
	rt := r.route(feature)
	if rt == nil || rt.reasoner == nil {
		return nil, unavailable(reasoner.ErrUnavailable, feature, rt)
	}
	return rt.reasoner, nil
}

// ImportEnvironment copies the STASH_OPENAI_* configuration into the store
// as a provider named "environment" and assigns every feature the
// environment currently serves to it. Running it again updates the same
// provider; it never overwrites an assignment that points elsewhere.
func (r *Router) ImportEnvironment(ctx context.Context) (Provider, []Assignment, error) {
	if r.store == nil || r.env == nil || strings.TrimSpace(r.env.BaseURL) == "" {
		return Provider{}, nil, errors.New("llm: STASH_OPENAI_BASE_URL is not configured; nothing to import")
	}
	timeout := int(r.env.RequestTimeout / time.Second)
	if timeout <= 0 {
		timeout = defaultRequestTimeoutSeconds
	}
	name, baseURL, apiKey := EnvProviderName, r.env.BaseURL, r.env.APIKey
	input := ProviderInput{Name: &name, BaseURL: &baseURL, RequestTimeoutSeconds: &timeout}
	if strings.TrimSpace(apiKey) != "" {
		input.APIKey = &apiKey
	}
	provider, err := r.store.FindProvider(ctx, name)
	switch {
	case errors.Is(err, ErrProviderNotFound):
		provider, err = r.store.CreateProvider(ctx, input)
	case err == nil:
		provider, err = r.store.UpdateProvider(ctx, provider.ID, input)
	}
	if err != nil {
		return Provider{}, nil, err
	}

	existing, err := r.store.ListAssignments(ctx)
	if err != nil {
		return Provider{}, nil, err
	}
	taken := map[Feature]Assignment{}
	for _, a := range existing {
		taken[a.Feature] = a
	}
	var imported []Assignment
	for _, info := range featureCatalog {
		if !r.env.serves(info.Kind) {
			continue
		}
		if current, ok := taken[info.Feature]; ok && current.ProviderID != provider.ID {
			continue
		}
		a := Assignment{Feature: info.Feature, ProviderID: provider.ID}
		if info.Kind == KindEmbedding {
			a.Model, a.Dimensions, a.ContextTokens = r.env.EmbeddingModel, r.env.VectorDim, r.env.EmbeddingContextTokens
		} else {
			a.Model, a.ContextTokens, a.ReservedTokens = r.env.ReasonerModel, r.env.ReasonerContextTokens, r.env.ReasonerReservedTokens
		}
		stored, err := r.store.SetAssignment(ctx, a)
		if err != nil {
			return Provider{}, nil, fmt.Errorf("assign %s: %w", info.Feature, err)
		}
		imported = append(imported, stored)
	}
	if err := r.Reload(ctx); err != nil {
		return Provider{}, nil, err
	}
	return provider, imported, nil
}

func unavailable(base error, feature Feature, rt *route) error {
	reason := "model routing has not been loaded"
	if rt != nil && rt.Error != "" {
		reason = rt.Error
	}
	return fmt.Errorf("%w: %s: %s", base, feature, reason)
}
