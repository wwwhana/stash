package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFirstRunSetupCreatesTheAdministratorOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := openAuthTestSchema(t, ctx, nil)
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret}, tokenPool: pool}
	if !p.SetupRequired(ctx) {
		t.Fatal("setup not required on an empty users table")
	}
	status := httptest.NewRecorder()
	p.HandleStatus(status, httptest.NewRequest(http.MethodGet, "/auth/status", nil))
	if !strings.Contains(status.Body.String(), `"setup_required":true`) || !strings.Contains(status.Body.String(), `"local_login":false`) {
		t.Fatalf("status = %s", status.Body.String())
	}
	// The server-rendered page offers the setup form instead of a token box.
	page := httptest.NewRecorder()
	p.HandleLogin(page, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `action="/auth/setup"`) || strings.Contains(page.Body.String(), `name="token"`) {
		t.Fatalf("login page status=%d body=%s", page.Code, page.Body.String())
	}
	tokenPage := httptest.NewRecorder()
	p.HandleLogin(tokenPage, httptest.NewRequest(http.MethodGet, "/auth/login?provider=token", nil))
	if !strings.Contains(tokenPage.Body.String(), `name="token"`) {
		t.Fatal("explicit token page lost its form")
	}

	post := func(body string, contentType string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/auth/setup", strings.NewReader(body))
		req.Header.Set("Content-Type", contentType)
		rec := httptest.NewRecorder()
		p.HandleSetup(rec, req)
		return rec
	}
	if rec := post(`{"username":"Root","password":"short","password_confirm":"short"}`, "application/json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("weak password status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post(`{"username":"root","password":"first-password-1","password_confirm":"other"}`, "application/json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatch status=%d", rec.Code)
	}
	if p.LocalLoginAvailable(ctx) {
		t.Fatal("rejected setup created an account")
	}
	rec := post(`{"username":"Root","display_name":"Root Admin","password":"first-password-1","password_confirm":"first-password-1"}`, "application/json")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"username":"root"`) {
		t.Fatalf("setup status=%d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("setup cookies = %#v", cookies)
	}
	verify := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	verify.AddCookie(cookies[0])
	if user, err := p.VerifyRequest(verify); err != nil || user != "root" {
		t.Fatalf("setup session = %q, %v", user, err)
	}
	if !p.IsAdmin(ctx, "root") || !p.HasPassword(ctx, "root") || p.SetupRequired(ctx) || !p.LocalLoginAvailable(ctx) {
		t.Fatal("first administrator is not an enabled admin with a password")
	}
	var reported map[string]any
	after := httptest.NewRecorder()
	p.HandleStatus(after, httptest.NewRequest(http.MethodGet, "/auth/status", nil))
	if err := json.NewDecoder(after.Body).Decode(&reported); err != nil || reported["setup_required"] != false || reported["local_login"] != true {
		t.Fatalf("status after setup = %v, %v", reported, err)
	}

	// Only the first visitor becomes the administrator.
	if rec := post(`{"username":"second","password":"second-password-1"}`, "application/json"); rec.Code != http.StatusConflict {
		t.Fatalf("second setup status=%d", rec.Code)
	}
	if _, err := p.CreateFirstAdmin(ctx, "third", "third-password-1", ""); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("CreateFirstAdmin after setup = %v", err)
	}
	if users, err := p.ListUsers(ctx); err != nil || len(users) != 1 {
		t.Fatalf("users after setup = %d, %v", len(users), err)
	}
	// The server-rendered form path answers with a redirect or the page.
	form := url.Values{"username": {"root"}, "password": {"first-password-1"}, "password_confirm": {"first-password-1"}}
	if rec := post(form.Encode(), "application/x-www-form-urlencoded"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "already exists") {
		t.Fatalf("form setup after setup status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Cross-origin posts are refused.
	req := httptest.NewRequest(http.MethodPost, "https://stash.example.com/auth/setup", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://attacker.example")
	cross := httptest.NewRecorder()
	p.HandleSetup(cross, req)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-origin setup status=%d", cross.Code)
	}
}

func TestFirstRunSetupFormOnEmptyDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := openAuthTestSchema(t, ctx, nil)
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret}, tokenPool: pool}
	form := url.Values{"username": {"owner"}, "display_name": {"Owner"}, "password": {"owner-password-1"}, "password_confirm": {"owner-password-1"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	p.HandleSetup(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("form setup status=%d location=%q cookies=%d body=%s", rec.Code, rec.Header().Get("Location"), len(rec.Result().Cookies()), rec.Body.String())
	}
	if user, err := p.GetUser(ctx, "owner"); err != nil || !user.IsAdmin || user.DisplayName != "Owner" || !user.HasPassword() {
		t.Fatalf("owner = %+v, %v", user, err)
	}
}
