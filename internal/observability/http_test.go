package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestInstrumentHTTPRecordsResponseAndPreservesFlushing(t *testing.T) {
	handler := InstrumentHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("instrumented writer does not preserve http.Flusher")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("created"))
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	if recorder.Body.String() != "created" {
		t.Fatalf("body = %q, want created", recorder.Body.String())
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
