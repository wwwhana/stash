package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAPIHandlerServesValidHTTPContract(t *testing.T) {
	response := httptest.NewRecorder()
	OpenAPIHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /openapi.json status = %d, want %d", response.Code, http.StatusOK)
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("GET /openapi.json content type = %q", response.Header().Get("Content-Type"))
	}
	var document struct {
		OpenAPI string                     `json:"openapi"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("GET /openapi.json returned invalid JSON: %v", err)
	}
	if document.OpenAPI != "3.0.3" {
		t.Fatalf("OpenAPI version = %q, want 3.0.3", document.OpenAPI)
	}
	for _, path := range []string{"/mcp", "/sse", "/message", "/healthz", "/readyz", "/metrics"} {
		if _, ok := document.Paths[path]; !ok {
			t.Fatalf("OpenAPI document is missing %s", path)
		}
	}
}

func TestSwaggerUIHandlerServesPinnedShell(t *testing.T) {
	response := httptest.NewRecorder()
	SwaggerUIHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/docs", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /docs status = %d, want %d", response.Code, http.StatusOK)
	}
	for _, marker := range []string{"swagger-ui-dist@5.11.10", "SwaggerUIBundle", "'/openapi.json'"} {
		if !strings.Contains(response.Body.String(), marker) {
			t.Fatalf("Swagger UI shell is missing %q", marker)
		}
	}
}

func TestDocumentationHandlersAreReadOnly(t *testing.T) {
	for name, handler := range map[string]http.Handler{
		"openapi": OpenAPIHandler(),
		"swagger": SwaggerUIHandler(),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s status = %d, want %d", name, response.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}
