package main

import (
	"net"
	"net/http"
	"strings"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
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
	prometheus.MustRegister(buildInfo, embeddingRetryAttempts, embeddingQueued, embeddingPending, mcpResponseLimited)
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
	embeddingPending.Set(float64(result.Pending))
}

// registerOperationalRoutes adds process-level status and metrics endpoints to
// the same listener that serves MCP, OAuth, and the web console.
func registerOperationalRoutes(mux *http.ServeMux, bc *bootstrap.Context) {
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", serviceStatusHandler(bc, "ok", false))
	mux.HandleFunc("/readyz", serviceStatusHandler(bc, "ready", true))
}

func serviceStatusHandler(bc *bootstrap.Context, body string, ready bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if bc == nil || bc.Brain == nil {
			http.Error(w, "service is not initialized", http.StatusServiceUnavailable)
			return
		}

		var err error
		if ready {
			err = bc.Brain.Ready(r.Context())
		} else {
			err = bc.Brain.Health(r.Context())
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}
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
