package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIPageRoutesServeTheWorkspace(t *testing.T) {
	handler := GetUIHandler()
	for _, path := range []string{"/", "/ui/goal-map", "/ui/plan?project=%2Fprojects%2Fdemo", "/ui/work-graph?status=doing"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
			}
			if !strings.Contains(response.Body.String(), `x-data="stashConsole()"`) {
				t.Fatalf("GET %s did not serve the workspace", path)
			}
		})
	}
}

func TestUIAssetsAndUnknownPathsKeepFileServerBehavior(t *testing.T) {
	handler := GetUIHandler()

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/route-state.js", nil))
	if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), "StashRouteState") {
		t.Fatalf("GET /route-state.js status = %d body = %q", asset.Code, asset.Body.String())
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/ui/not-a-page", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("GET /ui/not-a-page status = %d, want %d", missing.Code, http.StatusNotFound)
	}
}

func TestUIPageRoutesAreReadOnly(t *testing.T) {
	response := httptest.NewRecorder()
	GetUIHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/ui/plan", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /ui/plan status = %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}
