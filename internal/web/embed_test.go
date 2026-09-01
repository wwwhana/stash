package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIPageRoutesServeTheWorkspace(t *testing.T) {
	handler := GetUIHandler()
	for _, path := range []string{"/", "/ui/goal-map", "/ui/plan?project=%2Fprojects%2Fdemo", "/ui/monitor-alpine?project=%2Fprojects%2Fdemo&status=doing", "/ui/work-graph?status=doing"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusOK)
			}
			if !strings.Contains(response.Body.String(), `data-stash-vue-console`) {
				t.Fatalf("GET %s did not serve the Vue workspace", path)
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
		for _, marker := range []string{"data-stash-vue-console", "/vue-console.css", "/vue-monitor.js", "/vue-console.js"} {
			if !strings.Contains(body, marker) {
				t.Fatalf("Vue monitor entry point %s is missing %q", path, marker)
			}
		}
		if strings.Contains(body, `x-data="stashConsole()"`) {
			t.Fatalf("Vue route %s unexpectedly served the Alpine workspace", path)
		}
	}
}

func TestUIAssetsAndUnknownPathsKeepFileServerBehavior(t *testing.T) {
	handler := GetUIHandler()

	assets := map[string]string{
		"/search-utils.js":                "StashSearch",
		"/route-state.js":                 "StashRouteState",
		"/state-store.js":                 "StashStateStore",
		"/theme-view-model.js":            "StashThemeViewModel",
		"/theme-bootstrap.js":             "stashTheme",
		"/theme.css":                      "data-stash-theme",
		"/api-client.js":                  "StashApiClient",
		"/route-view-model.js":            "StashRouteViewModel",
		"/map-scope-view-model.js":        "StashMapScopeViewModel",
		"/work-board-scope-view-model.js": "StashWorkBoardScopeViewModel",
		"/work-graph-view-model.js":       "StashWorkGraphViewModel",
		"/graph-viewport-view-model.js":   "StashGraphViewportViewModel",
		"/work-graph-board.css":           "--graph-bg",
		"/work-monitor-view-model.js":     "StashWorkMonitorViewModel",
		"/project-monitor-view-model.js":  "StashProjectMonitorViewModel",
		"/goal-map-view-model.js":         "StashGoalMapViewModel",
		"/work-plan-view-model.js":        "StashWorkPlanViewModel",
		"/issue-execution-view-model.js":  "StashIssueExecutionViewModel",
		"/console-app.js":                 "StashConsoleApp",
		"/vue-monitor.js":                 "stash-vue-monitor",
		"/vue-console.js":                 "data-stash-vue-console",
		"/vue-console.css":                "--app-bg",
		"/vue-monitor.css":                "--vue-bg",
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
