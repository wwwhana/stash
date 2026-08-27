package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alash3al/stash/internal/bootstrap"
)

func TestConfiguredHTTPAddress(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantHost string
		wantPort string
	}{
		{name: "port only", raw: ":9107", wantHost: "0.0.0.0", wantPort: "9107"},
		{name: "host and port", raw: "127.0.0.1:9108", wantHost: "127.0.0.1", wantPort: "9108"},
		{name: "bare port", raw: "9109", wantHost: "0.0.0.0", wantPort: "9109"},
		{name: "invalid keeps fallback", raw: "not:an:address", wantHost: "0.0.0.0", wantPort: "8080"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port := configuredHTTPAddress(tt.raw, "0.0.0.0", "8080")
			if host != tt.wantHost || port != tt.wantPort {
				t.Fatalf("configuredHTTPAddress(%q) = (%q, %q), want (%q, %q)", tt.raw, host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestOperationalRoutesAreRegistered(t *testing.T) {
	mux := http.NewServeMux()
	registerOperationalRoutes(mux, nil)

	metrics := httptest.NewRecorder()
	mux.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want %d", metrics.Code, http.StatusOK)
	}
	if !strings.Contains(metrics.Body.String(), "stash_build_info") {
		t.Fatalf("GET /metrics did not include stash_build_info")
	}

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("GET %s status = %d, want %d", path, rr.Code, http.StatusServiceUnavailable)
			}
			if !strings.Contains(rr.Body.String(), "service is not initialized") {
				t.Fatalf("GET %s body = %q", path, rr.Body.String())
			}
		})
	}
}

func TestStashHTTPHandlerAllowsDisabledAuthentication(t *testing.T) {
	handler := newStashHTTPHandler(&bootstrap.Context{})

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/auth/status", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("GET /auth/status status = %d, want %d", status.Code, http.StatusOK)
	}
	if !strings.Contains(status.Body.String(), `"auth_mode":"none"`) {
		t.Fatalf("GET /auth/status body = %q", status.Body.String())
	}

	initialize := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	handler.ServeHTTP(initialize, request)
	if initialize.Code != http.StatusOK {
		t.Fatalf("POST /mcp initialize status = %d, want %d; body=%s", initialize.Code, http.StatusOK, initialize.Body.String())
	}
	if !strings.Contains(initialize.Body.String(), `"result"`) {
		t.Fatalf("POST /mcp initialize body = %q", initialize.Body.String())
	}
}
