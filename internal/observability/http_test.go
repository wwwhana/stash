package observability

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestInstrumentHTTPRecordsResponseAndPreservesFlushing(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler := InstrumentHTTP(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("instrumented writer does not preserve http.Flusher")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp?access_token=must-not-appear", nil)
	request.Header.Set("Authorization", "Bearer must-not-appear")
	request.Header.Set("X-Request-ID", "request-123")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if recorder.Body.String() != "created" {
		t.Fatalf("body = %q, want created", recorder.Body.String())
	}
	if got := recorder.Header().Get("X-Request-ID"); got != "request-123" {
		t.Fatalf("X-Request-ID = %q, want request-123", got)
	}

	accessLog := logs.String()
	for _, want := range []string{"msg=\"http access\"", "request_id=request-123", "method=POST", "path=/mcp", "route=/mcp", "status=201", "bytes=7"} {
		if !strings.Contains(accessLog, want) {
			t.Fatalf("access log %q does not contain %q", accessLog, want)
		}
	}
	for _, secret := range []string{"access_token", "must-not-appear", "Authorization"} {
		if strings.Contains(accessLog, secret) {
			t.Fatalf("access log leaked %q: %s", secret, accessLog)
		}
	}

	metrics, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	if !metricHasLabels(metrics, "stash_http_requests_total", map[string]string{
		"route": "/mcp", "method": http.MethodPost, "status": "201",
	}) {
		t.Fatal("HTTP request metric was not recorded")
	}
}

func TestInstrumentHTTPSuppressesAccessLogAboveDebugLevel(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler := InstrumentHTTP(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if logs.Len() != 0 {
		t.Fatalf("debug access log was emitted at info level: %s", logs.String())
	}
}

func TestRouteLabelKeepsUnknownPathsBounded(t *testing.T) {
	if got := routeLabel("/auth/callback/attacker-controlled"); got != "/auth/*" {
		t.Fatalf("route label = %q, want /auth/*", got)
	}
	if got := routeLabel("/users/secret"); got != "/other" {
		t.Fatalf("route label = %q, want /other", got)
	}
	if got := routeLabel("/.well-known/oauth-authorization-server/path"); got != "/.well-known/oauth-authorization-server" {
		t.Fatalf("OAuth metadata route label = %q", got)
	}
	if got := routeLabel("/oauth/token"); got != "/oauth/token" {
		t.Fatalf("OAuth token route label = %q", got)
	}
}

func metricHasLabels(metrics []*dto.MetricFamily, name string, labels map[string]string) bool {
	for _, family := range metrics {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			got := make(map[string]string, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				got[label.GetName()] = label.GetValue()
			}
			match := true
			for key, want := range labels {
				if got[key] != want {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}
