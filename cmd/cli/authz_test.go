package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/alash3al/stash/internal/auth"
	"github.com/alash3al/stash/internal/models"
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

func TestRemoteNamespaceResponsesUseLogicalPaths(t *testing.T) {
	ctx := context.WithValue(context.Background(), keyMode, "remote")
	ctx = context.WithValue(ctx, keySSOUser, "oidc-subject-1")
	owner := namespaceOwnerKey("oidc-subject-1")
	got := logicalNamespaces(ctx, []models.Namespace{
		{Slug: "/sso/" + owner, Name: "/sso/" + owner},
		{Slug: "/sso/" + owner + "/projects", Name: "/sso/" + owner + "/projects"},
		{Slug: "/sso/other-user", Name: "/sso/other-user"},
		{Slug: "/sso/" + owner + "/named", Name: "내 작업"},
	})

	if len(got) != 3 {
		t.Fatalf("logical namespaces = %#v, want three owned namespaces", got)
	}
	if got[0].Slug != "/" || got[0].Name != "/" {
		t.Fatalf("root namespace = %#v, want logical root", got[0])
	}
	if got[1].Slug != "/projects" || got[1].Name != "/projects" {
		t.Fatalf("parent namespace = %#v, want logical path", got[1])
	}
	if got[2].Slug != "/named" || got[2].Name != "내 작업" {
		t.Fatalf("named namespace = %#v, want logical slug and custom name", got[2])
	}
}

func TestLocalNamespaceResponsesKeepStoredPaths(t *testing.T) {
	ctx := context.WithValue(context.Background(), keyMode, "local")
	input := []models.Namespace{{Slug: "/sso/u_example/projects", Name: "/sso/u_example/projects"}}
	got := logicalNamespaces(ctx, input)
	if len(got) != 1 || got[0].Slug != input[0].Slug || got[0].Name != input[0].Name {
		t.Fatalf("local namespaces = %#v, want stored path", got)
	}
}

func TestRemoteNamespaceResponsesWithoutIdentityExposeNothing(t *testing.T) {
	ctx := context.WithValue(context.Background(), keyMode, "remote")
	got := logicalNamespaces(ctx, []models.Namespace{{Slug: "/sso/u_example/projects"}})
	if len(got) != 0 {
		t.Fatalf("remote namespaces without identity = %#v, want no namespaces", got)
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

func TestAuthenticatedHTTPRejectsCrossOriginWrite(t *testing.T) {
	h := authenticatedHTTP(&auth.Provider{}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("cross-origin request reached the MCP handler")
	}))
	req := httptest.NewRequest(http.MethodPost, "https://stash.example.com/mcp", nil)
	req.Header.Set("Origin", "https://attacker.example.com")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
