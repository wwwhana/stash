package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/alash3al/stash/internal/auth"
)

func TestNamespaceOwnerKeyIsStableAndPathSafe(t *testing.T) {
	first := namespaceOwnerKey("oidc-subject-1")
	if first != namespaceOwnerKey("oidc-subject-1") {
		t.Fatal("owner key is not stable")
	}
	if first == namespaceOwnerKey("oidc-subject-2") {
		t.Fatal("different subjects share an owner key")
	}
	if !regexp.MustCompile(`^u_[0-9a-f]{32}$`).MatchString(first) {
		t.Fatalf("owner key %q is not a valid namespace segment", first)
	}
}

func TestRemoteNamespacesAreIsolated(t *testing.T) {
	ctx := context.WithValue(context.Background(), keyMode, "remote")
	ctx = context.WithValue(ctx, keySSOUser, "oidc-subject-1")
	owner := namespaceOwnerKey("oidc-subject-1")
	got, err := resolveNamespaces(ctx, "/projects,team/work")
	if err != nil {
		t.Fatalf("resolve namespaces: %v", err)
	}
	want := []string{"/sso/" + owner + "/projects", "/sso/" + owner + "/team/work"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("namespaces = %#v, want %#v", got, want)
	}
}

func TestHTTPContextDoesNotTrustHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("X-Forwarded-User", "forged-user")
	ctx := httpContextFunc(context.Background(), req)
	if got, _ := ctx.Value(keyMode).(string); got != "remote" {
		t.Fatalf("mode = %q, want remote", got)
	}
	if _, ok := ctx.Value(keySSOUser).(string); ok {
		t.Fatal("unverified header became an authenticated user")
	}
}

func TestLocalHTTPMiddlewareMarksLocalMode(t *testing.T) {
	called := false
	h := authenticatedHTTP(nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got, _ := r.Context().Value(keyMode).(string); got != "local" {
			t.Errorf("mode = %q, want local", got)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if !called {
		t.Fatal("local middleware did not invoke the handler")
	}
}

func TestAuthenticatedHTTPRejectsMissingCredentials(t *testing.T) {
	h := authenticatedHTTP(&auth.Provider{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("unauthenticated request reached the MCP handler")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
