package observability

import (
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	eventsProcessed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "consolidation_events_processed_total",
			Help: "Total number of events processed by consolidation",
		}, []string{"namespace"},
	)
	factsCreated = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "consolidation_facts_created_total",
			Help: "Total number of facts created during consolidation",
		}, []string{"namespace"},
	)
	factsDeduplicated = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "consolidation_facts_deduplicated_total",
			Help: "Total number of facts skipped due to semantic deduplication",
		}, []string{"namespace"},
	)
	relationshipsCreated = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "consolidation_relationships_created_total",
			Help: "Total number of relationships extracted",
		}, []string{"namespace"},
	)
	llmCalls = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "consolidation_llm_calls_total",
			Help: "Total number of LLM calls made during consolidation",
		}, []string{"namespace"},
	)
	clustersFound = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "consolidation_clusters_found_total",
			Help: "Total number of clusters evaluated",
		}, []string{"namespace"},
	)
	eventsRead = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "consolidation_events_read_total",
			Help: "Total number of events read during consolidation",
		}, []string{"namespace"},
	)
	duration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "consolidation_duration_seconds",
			Help:    "Duration of consolidation runs",
			Buckets: prometheus.DefBuckets,
		}, []string{"namespace"},
	)
	errorsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "consolidation_errors_total",
			Help: "Number of errors encountered during consolidation",
		}, []string{"namespace"},
	)
	httpRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stash_http_requests_total",
			Help: "Total HTTP requests handled by Stash",
		}, []string{"route", "method", "status"},
	)
	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "stash_http_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method"},
	)
	authChecks = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stash_auth_checks_total",
			Help: "Total authentication checks by result",
		}, []string{"route", "result"},
	)
	mcpToolCalls = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stash_mcp_tool_calls_total",
			Help: "Total MCP tool calls by tool and result",
		}, []string{"tool", "result"},
	)
	mcpToolDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "stash_mcp_tool_call_duration_seconds",
			Help:    "MCP tool call duration in seconds",
			Buckets: prometheus.DefBuckets,
		}, []string{"tool"},
	)
	namespaceScopes = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stash_namespace_scope_resolutions_total",
			Help: "Namespace scope resolutions by access mode",
		}, []string{"mode", "scope"},
	)
	namespaceAuthorizations = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stash_namespace_authorizations_total",
			Help: "Namespace authorization checks by result",
		}, []string{"result"},
	)
)

// Observation carries the metrics that should be exported for a run.
type Observation struct {
	Namespace          string
	EventsRead         int
	EventsProcessed    int
	ClustersFound      int
	FactsCreated       int
	FactsDeduplicated  int
	RelationshipsFound int
	LLMCalls           int
	Duration           time.Duration
	Errors             int
}

// RecordConsolidation exports the provided observation to Prometheus.
func RecordConsolidation(obs Observation) {
	if obs.Namespace == "" {
		obs.Namespace = "default"
	}
	eventsProcessed.WithLabelValues(obs.Namespace).Add(float64(obs.EventsProcessed))
	eventsRead.WithLabelValues(obs.Namespace).Add(float64(obs.EventsRead))
	clustersFound.WithLabelValues(obs.Namespace).Add(float64(obs.ClustersFound))
	factsCreated.WithLabelValues(obs.Namespace).Add(float64(obs.FactsCreated))
	factsDeduplicated.WithLabelValues(obs.Namespace).Add(float64(obs.FactsDeduplicated))
	relationshipsCreated.WithLabelValues(obs.Namespace).Add(float64(obs.RelationshipsFound))
	llmCalls.WithLabelValues(obs.Namespace).Add(float64(obs.LLMCalls))
	duration.WithLabelValues(obs.Namespace).Observe(obs.Duration.Seconds())
	errorsTotal.WithLabelValues(obs.Namespace).Add(float64(obs.Errors))
}

// RecordHTTP records one completed HTTP request. Route values should be stable
// route names, never raw user-controlled paths.
func RecordHTTP(route, method string, status int, elapsed time.Duration) {
	route = stableLabel(route, "unknown")
	method = stableLabel(strings.ToUpper(method), "UNKNOWN")
	if status < 100 || status > 599 {
		status = 500
	}
	statusLabel := strconv.Itoa(status)
	httpRequests.WithLabelValues(route, method, statusLabel).Inc()
	httpRequestDuration.WithLabelValues(route, method).Observe(elapsed.Seconds())
}

// RecordAuthCheck records authentication outcomes without exposing user IDs.
func RecordAuthCheck(route, result string) {
	authChecks.WithLabelValues(stableLabel(route, "unknown"), stableLabel(result, "unknown")).Inc()
}

// RecordMCPToolCall records a completed tool handler invocation. Tool names are
// bounded by the registered MCP tool set and are safe to use as labels.
func RecordMCPToolCall(tool, result string, elapsed time.Duration) {
	tool = stableLabel(tool, "unknown")
	result = stableLabel(result, "unknown")
	mcpToolCalls.WithLabelValues(tool, result).Inc()
	mcpToolDuration.WithLabelValues(tool).Observe(elapsed.Seconds())
}

// RecordNamespaceScope records whether a request was resolved to the local
// unscoped store or to a verified user's namespace.
func RecordNamespaceScope(mode, scope string) {
	namespaceScopes.WithLabelValues(stableLabel(mode, "unknown"), stableLabel(scope, "unknown")).Inc()
}

// RecordNamespaceAuthorization records object-level namespace checks.
func RecordNamespaceAuthorization(result string) {
	namespaceAuthorizations.WithLabelValues(stableLabel(result, "unknown")).Inc()
}

func stableLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if len(value) > 64 {
		return value[:64]
	}
	return value
}
