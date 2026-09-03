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
	for _, marker := range []string{
		"swagger-ui-dist@5.32.14",
		`integrity="sha384-fgyWYkUAamzuI8mJFu/xpRP0JWCJRwkwUwsYDoOYVHUJ8NQE5cENn8ib3ppwFFSX"`,
		`integrity="sha384-Dt83RhU85ZmX7werw9uTFCzmauXUoSyx3pdzTQMABtsnFmooJy4Vz9/ACh7n5m1A"`,
		`/swagger-init.js`,
	} {
		if !strings.Contains(response.Body.String(), marker) {
			t.Fatalf("Swagger UI shell is missing %q", marker)
		}
	}
	if strings.Contains(response.Body.String(), "persistAuthorization: true") {
		t.Fatal("Swagger UI persists authorization data")
	}
	if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'self' https://cdn.jsdelivr.net") {
		t.Fatalf("Swagger UI content security policy = %q", got)
	}

	initResponse := httptest.NewRecorder()
	SwaggerInitHandler().ServeHTTP(initResponse, httptest.NewRequest(http.MethodGet, "/swagger-init.js", nil))
	if initResponse.Code != http.StatusOK || !strings.Contains(initResponse.Body.String(), "SwaggerUIBundle") || !strings.Contains(initResponse.Body.String(), "persistAuthorization: false") {
		t.Fatalf("Swagger init status=%d body=%q", initResponse.Code, initResponse.Body.String())
	}
}

func TestDocumentationHandlersAreReadOnly(t *testing.T) {
	for name, handler := range map[string]http.Handler{
		"openapi":      OpenAPIHandler(),
		"swagger":      SwaggerUIHandler(),
		"swagger init": SwaggerInitHandler(),
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
