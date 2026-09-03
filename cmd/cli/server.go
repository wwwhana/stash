package main

import (
	"net"
	"net/http"
	"strings"

	"github.com/alash3al/stash/internal/auth"
	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/web"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	buildInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "stash_build_info",
			Help: "Build information, value is always 1",
		},
		[]string{"version"},
	)
	embeddingRetryAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "stash_embedding_retry_attempts_total",
			Help: "Durable embedding retry attempts by result",
		},
		[]string{"result"},
	)
	embeddingRetryPaused = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stash_embedding_retry_paused_total",
			Help: "Embedding rows paused after reaching the retry attempt limit",
		},
	)
	embeddingQueued = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stash_embedding_queued_total",
			Help: "Memories durably queued after an embedding failure",
		},
	)
	embeddingPending = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "stash_embedding_pending",
			Help: "Episodes and facts currently waiting for embedding",
		},
	)
	mcpResponseLimited = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "stash_mcp_response_limited_total",
			Help: "MCP tool responses split or omitted to stay within the configured byte limit",
		},
	)
)

func init() {
	prometheus.MustRegister(buildInfo, embeddingRetryAttempts, embeddingRetryPaused, embeddingQueued, embeddingPending, mcpResponseLimited)
	buildInfo.WithLabelValues("0.2.8").Set(1)
}

func recordEmbeddingQueued() {
	embeddingQueued.Inc()
	embeddingPending.Inc()
}

func recordEmbeddingRetryMetrics(result brain.EmbeddingRetryResult) {
	if result.Indexed > 0 {
		embeddingRetryAttempts.WithLabelValues("indexed").Add(float64(result.Indexed))
	}
	if result.Failed > 0 {
		embeddingRetryAttempts.WithLabelValues("failed").Add(float64(result.Failed))
	}
	if result.Paused > 0 {
		embeddingRetryPaused.Add(float64(result.Paused))
	}
	embeddingPending.Set(float64(result.Pending))
}

// registerOperationalRoutes adds process-level status and metrics endpoints to
// the same listener that serves MCP, OAuth, and the web console.
func registerOperationalRoutes(mux *http.ServeMux, bc *bootstrap.Context) {
	// Metrics share the MCP listener. Keep the health probes public for the
	// load balancer, but require the same credential as MCP before exposing
	// process and workload details.
	var provider *auth.Provider
	if bc != nil {
		provider = bc.Auth
	}
	mux.Handle("/metrics", authenticatedHTTP(provider, promhttp.Handler()))
	mux.HandleFunc("/healthz", serviceStatusHandler(bc, "ok", false))
	mux.HandleFunc("/readyz", serviceStatusHandler(bc, "ready", true))
}

func registerDocumentationRoutes(mux *http.ServeMux) {
	mux.Handle("/openapi.json", web.OpenAPIHandler())
	mux.Handle("/swagger-init.js", web.SwaggerInitHandler())
	swagger := web.SwaggerUIHandler()
	for _, path := range []string{"/docs", "/docs/", "/swagger", "/swagger/"} {
		mux.Handle(path, swagger)
	}
}

func serviceStatusHandler(bc *bootstrap.Context, body string, ready bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bc == nil || bc.Brain == nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		var err error
		if ready {
			err = bc.Brain.Ready(r.Context())
		} else {
			err = bc.Brain.Health(r.Context())
		}
		if err != nil {
			if bc.Logger != nil {
				bc.Logger.Warn("service status check failed", "ready", ready, "error", err)
			}
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
}

func limitRequestBody(next http.Handler, maxBytes int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch) {
			if r.ContentLength > maxBytes {
				http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// configuredHTTPAddress parses STASH_HTTP_ADDR. It accepts :8080,
// 127.0.0.1:8080, and 8080 forms.
func configuredHTTPAddress(raw, fallbackHost, fallbackPort string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallbackHost, fallbackPort
	}
	if strings.HasPrefix(raw, ":") && len(raw) > 1 {
		return fallbackHost, raw[1:]
	}
	if host, port, err := net.SplitHostPort(raw); err == nil && port != "" {
		if host == "" {
			host = fallbackHost
		}
		return host, port
	}
	if !strings.Contains(raw, ":") {
		return fallbackHost, raw
	}
	return fallbackHost, fallbackPort
}
