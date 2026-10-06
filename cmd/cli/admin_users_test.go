package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/auth"
	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/brain"
	"github.com/alash3al/stash/internal/config"
	"github.com/alash3al/stash/internal/db"
)

// TestAdminUserRoutes drives the user and token management API as the
// console does: an administrator session creates, edits, and locks users,
// lists and revokes their tokens, and is refused when it would lock itself
// out.
func TestAdminUserRoutes(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("STASH_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("set STASH_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := db.OpenPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer pool.Close()
	suffix := time.Now().UnixNano()
	usernames := []string{usernameFor("root", suffix), usernameFor("eve", suffix), usernameFor("sso", suffix)}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM users WHERE username = ANY($1)`, usernames)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM auth_tokens WHERE subject = ANY($1)`, usernames)
	}()

	provider, err := auth.Init(ctx, auth.Config{Mode: "token", APISecret: testAuthSecret})
	if err != nil {
		t.Fatal(err)
	}
	provider.SetTokenPool(pool)
	root := usernames[0]
	if _, err := provider.CreateLocalUser(ctx, root, "root-password-1", "Root", true); err != nil {
		t.Fatal(err)
	}
	rootToken, _, err := provider.IssueAPIToken(ctx, root, "console test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	bc := &bootstrap.Context{Config: &config.Config{AuthMode: "token"}, Brain: &brain.Brain{}, Auth: provider}
	registerAdminRoutes(mux, bc)
	call := func(method, path string, body any) (int, map[string]any) {
		var reader *strings.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = strings.NewReader(string(raw))
		} else {
			reader = strings.NewReader("")
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+rootToken)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var decoded map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &decoded)
		return rec.Code, decoded
	}

	eve := usernames[1]
	if code, body := call(http.MethodPost, "/admin/users", map[string]any{"username": eve, "display_name": "Eve", "password": "eve-password-1"}); code != http.StatusCreated || body["username"] != eve {
		t.Fatalf("create user: %d %v", code, body)
	}
	if code, _ := call(http.MethodPost, "/admin/users", map[string]any{"username": eve, "password": "eve-password-1"}); code != http.StatusConflict {
		t.Fatalf("duplicate user status = %d", code)
	}
	if code, _ := call(http.MethodPost, "/admin/users", map[string]any{"username": "Bad Name", "password": "eve-password-1"}); code != http.StatusBadRequest {
		t.Fatalf("invalid username status = %d", code)
	}
	ssoUser := usernames[2]
	if code, body := call(http.MethodPost, "/admin/users", map[string]any{"username": ssoUser}); code != http.StatusCreated || len(body["identities"].([]any)) != 0 {
		t.Fatalf("create user without password: %d %v", code, body)
	}
	if code, body := call(http.MethodPost, "/admin/users/"+ssoUser+"/password", map[string]any{"password": "sso-password-1"}); code != http.StatusOK || len(body["identities"].([]any)) != 1 {
		t.Fatalf("set password: %d %v", code, body)
	}
	if code, _ := call(http.MethodPost, "/admin/users/"+ssoUser+"/password", map[string]any{"password": "short"}); code != http.StatusBadRequest {
		t.Fatalf("weak password status = %d", code)
	}
	code, body := call(http.MethodGet, "/admin/users", nil)
	if code != http.StatusOK || body["actor"] != root {
		t.Fatalf("list users: %d %v", code, body)
	}
	listed := map[string]bool{}
	for _, item := range body["users"].([]any) {
		listed[item.(map[string]any)["username"].(string)] = true
	}
	if !listed[root] || !listed[eve] || !listed[ssoUser] {
		t.Fatalf("list is missing users: %v", listed)
	}

	// Tokens are listed newest first and can be revoked for someone else.
	first, _, err := provider.IssueAPIToken(ctx, eve, "first", 0)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, err := provider.IssueAPIToken(ctx, eve, "second", 0); err != nil {
		t.Fatal(err)
	}
	code, body = call(http.MethodGet, "/admin/users/"+eve+"/tokens", nil)
	tokens := body["tokens"].([]any)
	if code != http.StatusOK || len(tokens) != 2 || tokens[0].(map[string]any)["name"] != "second" {
		t.Fatalf("token list: %d %v", code, body)
	}
	firstID := int64(tokens[1].(map[string]any)["id"].(float64))
	if code, body := call(http.MethodPost, "/admin/users/"+eve+"/tokens/"+itoa(firstID)+"/revoke", nil); code != http.StatusOK || body["revoked"] != true {
		t.Fatalf("revoke: %d %v", code, body)
	}
	if _, err := provider.VerifyBearerToken(ctx, first); err == nil {
		t.Fatal("revoked token still works")
	}
	if code, _ := call(http.MethodPost, "/admin/users/"+eve+"/tokens/999999999/revoke", nil); code != http.StatusNotFound {
		t.Fatalf("revoke unknown token status = %d", code)
	}

	// Flags change for others, never for yourself.
	if code, body := call(http.MethodPut, "/admin/users/"+eve, map[string]any{"is_admin": true, "disabled": true}); code != http.StatusOK || body["is_admin"] != true || body["disabled"] != true {
		t.Fatalf("update user: %d %v", code, body)
	}
	for _, change := range []map[string]any{{"is_admin": false}, {"disabled": true}} {
		if code, _ := call(http.MethodPut, "/admin/users/"+root, change); code != http.StatusConflict {
			t.Fatalf("self lockout change %v status = %d", change, code)
		}
	}
	if code, _ := call(http.MethodPut, "/admin/users/"+root, map[string]any{"display_name": "Root Admin"}); code != http.StatusOK {
		t.Fatalf("self display name status = %d", code)
	}
	if code, _ := call(http.MethodDelete, "/admin/users/"+root, nil); code != http.StatusConflict {
		t.Fatalf("self delete status = %d", code)
	}
	if code, _ := call(http.MethodDelete, "/admin/users/"+eve, nil); code != http.StatusOK {
		t.Fatalf("delete user status = %d", code)
	}
	if code, _ := call(http.MethodDelete, "/admin/users/"+eve, nil); code != http.StatusNotFound {
		t.Fatalf("second delete status = %d", code)
	}

	// A non-administrator cannot reach any of it.
	eveToken, _, err := provider.IssueAPIToken(ctx, ssoUser, "eve", 0)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/users", nil)
	req.Header.Set("Authorization", "Bearer "+eveToken)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d", rec.Code)
	}
}

func usernameFor(base string, suffix int64) string {
	return base + "-" + itoa(suffix)
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}
