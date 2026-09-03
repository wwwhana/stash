package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/config"
)

func TestAdminTokenMatchesExactHeader(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/admin/maintenance/embeddings", nil)
	request.Header.Set("X-Stash-Admin-Token", " maintenance-secret ")
	if !adminTokenMatches(request, "maintenance-secret") {
		t.Fatal("trimmed admin token should match")
	}
	request.Header.Set("X-Stash-Admin-Token", "maintenance-secret-extra")
	if adminTokenMatches(request, "maintenance-secret") {
		t.Fatal("different admin token must not match")
	}
	request.Header.Set("Authorization", "Bearer maintenance-secret")
	request.Header.Del("X-Stash-Admin-Token")
	if adminTokenMatches(request, "maintenance-secret") {
		t.Fatal("bearer token must not be accepted as the admin credential")
	}
}

func TestAdminSubjectMatchesCommaSeparatedList(t *testing.T) {
	if !adminSubjectMatches("user-2", "user-1, user-2") {
		t.Fatal("configured subject should match")
	}
	if adminSubjectMatches("user-", "user-1, user-2") {
		t.Fatal("subject matching must be exact")
	}
	if adminSubjectMatches("user-1", "") {
		t.Fatal("empty configuration must not grant admin access")
	}
}

func TestAdminOnlyHTTPRequiresConfiguredCredential(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	bc := &bootstrap.Context{Config: &config.Config{AdminToken: "maintenance-secret"}, Brain: &brain.Brain{}}
	handler := adminOnlyHTTP(bc, next)

	wrong := httptest.NewRecorder()
	wrongRequest := httptest.NewRequest(http.MethodGet, "/admin/maintenance/embeddings", nil)
	wrongRequest.Header.Set("X-Stash-Admin-Token", "wrong")
	handler.ServeHTTP(wrong, wrongRequest)
	if wrong.Code != http.StatusUnauthorized || called {
		t.Fatalf("wrong admin token status=%d called=%v, want 401 and no handler call", wrong.Code, called)
	}

	valid := httptest.NewRecorder()
	validRequest := httptest.NewRequest(http.MethodGet, "/admin/maintenance/embeddings", nil)
	validRequest.Header.Set("X-Stash-Admin-Token", "maintenance-secret")
	handler.ServeHTTP(valid, validRequest)
	if valid.Code != http.StatusOK || !called {
		t.Fatalf("valid admin token status=%d called=%v, want 200 and handler call", valid.Code, called)
	}
}

func TestAdminOnlyHTTPRejectsCrossOriginWrite(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	bc := &bootstrap.Context{Config: &config.Config{AdminToken: "maintenance-secret"}, Brain: &brain.Brain{}}
	handler := adminOnlyHTTP(bc, next)

	request := httptest.NewRequest(http.MethodPost, "https://stash.example.com/admin/maintenance/embeddings/reindex", nil)
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("X-Stash-Admin-Token", "maintenance-secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || called {
		t.Fatalf("cross-origin admin write status=%d called=%v, want 403 and no handler call", response.Code, called)
	}
}
