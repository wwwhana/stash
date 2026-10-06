package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIPageRoutesServeTheWorkspace(t *testing.T) {
	handler := GetUIHandler()
	for _, path := range []string{"/", "/ui/goal-map", "/ui/plan?project=%2Fprojects%2Fdemo", "/ui/monitor-alpine?project=%2Fprojects%2Fdemo&status=doing", "/ui/work-graph?status=doing", "/ui/tokens", "/ui/llm"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
			}
			if !strings.Contains(response.Body.String(), `data-stash-vue-console`) {
				t.Fatalf("GET %s did not serve the Vue workspace", path)
			}
			if got := response.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") || !strings.Contains(got, "script-src 'self' 'unsafe-eval'") || strings.Contains(got, "script-src 'self' 'unsafe-inline'") {
				t.Fatalf("GET %s content security policy = %q", path, got)
			}
			if response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("GET %s is missing nosniff", path)
			}
		})
	}
}

func TestVueMonitorRouteServesItsOwnEntryPoint(t *testing.T) {
	for _, path := range []string{"/ui/monitor?project=%2Fprojects%2Fdemo", "/ui/monitor-vue?project=%2Fprojects%2Fdemo"} {
		response := httptest.NewRecorder()
		GetUIHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
		}
		body := response.Body.String()
		for _, marker := range []string{"data-stash-vue-console", "/vue-console.css", "/vue-runtime.js", "/vue-console.js"} {
			if !strings.Contains(body, marker) {
				t.Fatalf("Vue monitor entry point %s is missing %q", path, marker)
			}
		}
	}
}

func TestUIAssetsAndUnknownPathsKeepFileServerBehavior(t *testing.T) {
	handler := GetUIHandler()

	assets := map[string]string{
		"/search-utils.js":           "StashSearch",
		"/console-i18n.js":           "StashI18n",
		"/route-state.js":            "StashRouteState",
		"/api-client.js":             "StashApiClient",
		"/goal-map-layout.js":        "StashGoalMap",
		"/work-graph-layout.js":      "StashWorkGraph",
		"/vue-runtime.js":            "StashVueRuntime",
		"/vue-console.js":            "data-stash-vue-console",
		"/vue-console-view-model.js": "StashVueConsoleViewModel",
		"/vue-bootstrap.js":          "stashConsoleApplyTheme",
		"/vue-console.css":           "--app-bg",
	}
	for path, marker := range assets {
		t.Run(path, func(t *testing.T) {
			asset := httptest.NewRecorder()
			handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, path, nil))
			if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), marker) {
				t.Fatalf("GET %s status = %d body = %q", path, asset.Code, asset.Body.String())
			}
		})
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

func TestVueConsoleDoesNotRestoreBrowserStoredTokens(t *testing.T) {
	for _, path := range []string{"/vue-console.js", "/vue-console-view-model.js"} {
		response := httptest.NewRecorder()
		GetUIHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET /vue-console.js status = %d, want %d", response.Code, http.StatusOK)
		}
		if strings.Contains(response.Body.String(), "stash.apiToken") {
			t.Fatal("Vue console still reads an API token from browser storage")
		}
	}
}
