package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/auth"
	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/observability"
	"github.com/mark3labs/mcp-go/mcp"
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

func TestDocumentationRoutesAreRegistered(t *testing.T) {
	mux := http.NewServeMux()
	registerDocumentationRoutes(mux)

	for _, path := range []string{"/openapi.json", "/docs", "/docs/", "/swagger", "/swagger/"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
			}
		})
	}
}

func TestOperationalMetricsRequireHTTPAuthentication(t *testing.T) {
	mux := http.NewServeMux()
	registerOperationalRoutes(mux, &bootstrap.Context{Auth: &auth.Provider{}})

	metrics := httptest.NewRecorder()
	mux.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusUnauthorized {
		t.Fatalf("GET /metrics status = %d, want %d", metrics.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(metrics.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("GET /metrics did not advertise bearer authentication: %q", metrics.Header().Get("WWW-Authenticate"))
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
	if !strings.Contains(initialize.Body.String(), `"instructions"`) || !strings.Contains(initialize.Body.String(), "resume_project") {
		t.Fatalf("POST /mcp initialize did not include agent automation instructions: %q", initialize.Body.String())
	}
}

func TestStashHTTPHandlerUsesNativeMCPToken(t *testing.T) {
	provider, err := auth.Init(context.Background(), auth.Config{Mode: "token", APISecret: "test-secret"})
	if err != nil {
		t.Fatalf("init token auth: %v", err)
	}
	token, err := auth.GenerateAPIToken("agent-1", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	handler := newStashHTTPHandler(&bootstrap.Context{Auth: provider})
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"result"`) {
		t.Fatalf("native MCP request status = %d, body = %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`))
	request.Header.Set("Authorization", "Bearer upstream-oidc-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != `Bearer realm="stash"` {
		t.Fatalf("OIDC MCP request status = %d, challenge = %q", response.Code, response.Header().Get("WWW-Authenticate"))
	}
}

func TestObserveMCPToolStopsStalledHandler(t *testing.T) {
	middleware := observeMCPTool(nil, 20*time.Millisecond)
	handler := middleware(func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	started := time.Now()
	_, err := handler(context.Background(), mcp.CallToolRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled tool error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled tool took %s, want it to stop promptly", elapsed)
	}
}

func TestObserveMCPToolLogsCallAndErrorWithoutArguments(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	middleware := observeMCPTool(logger, time.Second)
	handler := middleware(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New("provider unavailable")
	})
	request := mcp.CallToolRequest{}
	request.Params.Name = "recall"
	ctx := observability.WithRequestID(context.Background(), "request-123")
	_, err := handler(ctx, request)
	if err == nil {
		t.Fatal("tool succeeded, want error")
	}

	logText := logs.String()
	for _, want := range []string{"level=WARN", "msg=\"mcp tool call failed\"", "tool=recall", "outcome=error", "request_id=request-123", "error=\"provider unavailable\""} {
		if !strings.Contains(logText, want) {
			t.Fatalf("MCP tool log %q does not contain %q", logText, want)
		}
	}
	if strings.Contains(logText, "arguments") {
		t.Fatalf("MCP tool log should not include arguments: %s", logText)
	}
}
