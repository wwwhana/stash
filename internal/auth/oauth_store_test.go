package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func openOAuthStoreTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("STASH_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("set STASH_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database URL")
	}
	schema := fmt.Sprintf("oauth_store_test_%d", time.Now().UnixNano())
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
		pool.Close()
	})
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	sql, err := os.ReadFile("../db/migrations/00042_add_oauth_persistence.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(sql), "-- +goose Down")
	if _, err := pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestOAuthClientsAndRefreshTokensSurviveRestart(t *testing.T) {
	pool := openOAuthStoreTestPool(t)
	before := newLocalOAuthProvider()
	before.tokenPool = pool

	registration := `{"client_name":"Codex","redirect_uris":["http://127.0.0.1:43123/callback"],"token_endpoint_auth_method":"client_secret_post"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(registration))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	before.HandleOAuthRegister(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registration status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var registered struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&registered); err != nil || registered.ClientSecret == "" {
		t.Fatalf("decode registration: %v", err)
	}

	resource := "https://stash.example.com/mcp"
	issueReq := httptest.NewRequest(http.MethodPost, "https://stash.example.com/oauth/token", nil)
	issueRec := httptest.NewRecorder()
	before.issueOAuthTokens(issueRec, issueReq, "subject-1", registered.ClientID, resource, "")
	var issued struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(issueRec.Body).Decode(&issued); err != nil || issued.RefreshToken == "" {
		t.Fatalf("issue tokens: %v, body = %s", err, issueRec.Body.String())
	}
	if len(before.refreshTokens) != 0 {
		t.Fatal("refresh token was kept in memory instead of durable storage")
	}

	// A fresh provider has empty in-memory maps, just like a restarted server.
	after := newLocalOAuthProvider()
	after.tokenPool = pool
	refresh := func(secret string) *httptest.ResponseRecorder {
		form := url.Values{
			"grant_type":    {"refresh_token"},
			"client_id":     {registered.ClientID},
			"client_secret": {secret},
			"refresh_token": {issued.RefreshToken},
			"resource":      {resource},
		}
		req := httptest.NewRequest(http.MethodPost, "https://stash.example.com/oauth/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		after.HandleOAuthToken(rec, req)
		return rec
	}
	if got := refresh("wrong-secret"); got.Code == http.StatusOK {
		t.Fatal("restored client accepted the wrong secret")
	}
	if got := refresh(registered.ClientSecret); got.Code != http.StatusOK {
		t.Fatalf("refresh after restart status = %d, body = %s", got.Code, got.Body.String())
	}
	if got := refresh(registered.ClientSecret); got.Code != http.StatusBadRequest {
		t.Fatalf("rotated refresh token replay status = %d, want %d", got.Code, http.StatusBadRequest)
	}
}
