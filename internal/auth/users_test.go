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

func localLogin(t *testing.T, p *Provider, username, password string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(url.Values{"username": {username}, "password": {password}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.7:4455"
	rec := httptest.NewRecorder()
	p.HandleLogin(rec, req)
	return rec
}

func TestUsersAndIdentities(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := openAuthTestSchema(t, ctx, nil)
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret, Issuer: "https://sso.example.com", AdminSubjects: "ops-bot"}, tokenPool: pool}

	if p.LocalLoginAvailable(ctx) {
		t.Fatal("login form offered before any user has a password")
	}
	if _, err := p.CreateLocalUser(ctx, "Bad Name", "correct horse battery", "", false); !errors.Is(err, ErrInvalidUsername) {
		t.Fatalf("invalid username error = %v", err)
	}
	if _, err := p.CreateLocalUser(ctx, "alice", "short", "", false); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("weak password error = %v", err)
	}
	alice, err := p.CreateLocalUser(ctx, " Alice ", "correct horse battery", "Alice Liddell", true)
	if err != nil {
		t.Fatal(err)
	}
	if alice.Username != "alice" || !alice.IsAdmin || !alice.HasPassword() || len(alice.Identities) != 1 || alice.Identities[0].Kind != "password" || alice.Identities[0].Subject != "" {
		t.Fatalf("created user = %+v", alice)
	}
	if _, err := p.CreateLocalUser(ctx, "alice", "another password", "", false); !errors.Is(err, ErrUserExists) {
		t.Fatalf("duplicate user error = %v", err)
	}
	if !p.LocalLoginAvailable(ctx) || !p.IsAdmin(ctx, "alice") || !p.HasPassword(ctx, "alice") || !p.IsAdmin(ctx, "ops-bot") || p.IsAdmin(ctx, "nobody") {
		t.Fatal("user flags are not reported")
	}

	// The login page shows the password form first and keeps the token form a link away.
	page := httptest.NewRecorder()
	p.HandleLogin(page, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `name="username"`) || !strings.Contains(page.Body.String(), "provider=token") {
		t.Fatalf("login page status=%d body=%s", page.Code, page.Body.String())
	}
	tokenPage := httptest.NewRecorder()
	p.HandleLogin(tokenPage, httptest.NewRequest(http.MethodGet, "/auth/login?provider=token", nil))
	if !strings.Contains(tokenPage.Body.String(), `name="token"`) || !strings.Contains(tokenPage.Body.String(), "provider=local") {
		t.Fatalf("token page body=%s", tokenPage.Body.String())
	}

	if rec := localLogin(t, p, "alice", "wrong password"); rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("wrong password status=%d cookies=%d", rec.Code, len(rec.Result().Cookies()))
	}
	if rec := localLogin(t, p, "ghost", "correct horse battery"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user status=%d", rec.Code)
	}
	rec := localLogin(t, p, "ALICE", "correct horse battery")
	cookies := rec.Result().Cookies()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" || len(cookies) != 1 || cookies[0].Name != sessionCookieName {
		t.Fatalf("login status=%d location=%q cookies=%d", rec.Code, rec.Header().Get("Location"), len(cookies))
	}
	session := cookies[0]
	verify := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
	verify.AddCookie(session)
	if user, err := p.VerifyRequest(verify); err != nil || user != "alice" {
		t.Fatalf("session user = %q, %v", user, err)
	}
	status := httptest.NewRecorder()
	p.HandleStatus(status, verify)
	var reported map[string]any
	if err := json.NewDecoder(status.Body).Decode(&reported); err != nil {
		t.Fatal(err)
	}
	if reported["user"] != "alice" || reported["admin"] != true || reported["has_password"] != true || reported["local_login"] != true || reported["sso_login"] != false {
		t.Fatalf("status = %v", reported)
	}
	if got, err := p.GetUser(ctx, "alice"); err != nil || got.LastLoginAt == nil || got.Identities[0].LastUsedAt == nil {
		t.Fatalf("login did not record last use: %+v, %v", got, err)
	}

	// A password change needs the current password and a browser session.
	changePassword := func(current, next string, cookie *http.Cookie, bearer string) int {
		body, _ := json.Marshal(map[string]string{"current_password": current, "new_password": next})
		req := httptest.NewRequest(http.MethodPost, "/auth/password", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.8:1"
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		p.HandlePassword(rec, req)
		return rec.Code
	}
	if code := changePassword("correct horse battery", "a brand new password", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous password change status=%d", code)
	}
	if code := changePassword("wrong", "a brand new password", session, ""); code != http.StatusUnauthorized {
		t.Fatalf("wrong current password status=%d", code)
	}
	if code := changePassword("correct horse battery", "short", session, ""); code != http.StatusBadRequest {
		t.Fatalf("weak new password status=%d", code)
	}
	if code := changePassword("correct horse battery", "a brand new password", session, ""); code != http.StatusNoContent {
		t.Fatalf("password change status=%d", code)
	}
	if rec := localLogin(t, p, "alice", "correct horse battery"); rec.Code != http.StatusUnauthorized {
		t.Fatal("old password still works")
	}
	if rec := localLogin(t, p, "alice", "a brand new password"); rec.Code != http.StatusSeeOther {
		t.Fatal("new password does not work")
	}

	// Disabling takes effect on the next request, even for an open session.
	disabled := true
	if _, err := p.UpdateUser(ctx, "alice", UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.VerifyRequest(verify); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("disabled user session error = %v", err)
	}
	if rec := localLogin(t, p, "alice", "a brand new password"); rec.Code != http.StatusUnauthorized {
		t.Fatal("disabled user could log in")
	}
	if p.IsAdmin(ctx, "alice") || p.LocalLoginAvailable(ctx) {
		t.Fatal("disabled user still counts as administrator or login option")
	}

	// Seeding re-enables and promotes an existing user without touching its password.
	if created, err := p.SeedLocalAdmin(ctx, "alice", "seed password ignored"); err != nil || created {
		t.Fatalf("seed existing = %v, %v", created, err)
	}
	if rec := localLogin(t, p, "alice", "a brand new password"); rec.Code != http.StatusSeeOther {
		t.Fatal("seeding changed the stored password or left the user disabled")
	}
	if created, err := p.SeedLocalAdmin(ctx, "root", "initial root password"); err != nil || !created {
		t.Fatalf("seed new = %v, %v", created, err)
	}

	// SSO subjects are provisioned as users on first login and linked afterwards.
	username, err := p.resolveOIDCUser(ctx, "https://sso.example.com", "sub-42", oidcProfile{Name: "Bob Builder", Email: "bob@example.com"})
	if err != nil || username != "sub-42" {
		t.Fatalf("provisioned user = %q, %v", username, err)
	}
	bob, err := p.GetUser(ctx, "sub-42")
	if err != nil || bob.DisplayName != "Bob Builder" || bob.HasPassword() || len(bob.Identities) != 1 || bob.Identities[0].Kind != "oidc" || bob.Identities[0].Issuer != "https://sso.example.com" || bob.Identities[0].Subject != "sub-42" {
		t.Fatalf("provisioned user = %+v, %v", bob, err)
	}
	if again, err := p.resolveOIDCUser(ctx, "https://sso.example.com", "sub-42", oidcProfile{Name: "Renamed"}); err != nil || again != "sub-42" {
		t.Fatalf("second login = %q, %v", again, err)
	}
	if users, err := p.ListUsers(ctx); err != nil || len(users) != 3 {
		t.Fatalf("users = %d, %v", len(users), err)
	}
	// A subject that matches an existing username is the same person.
	if linked, err := p.resolveOIDCUser(ctx, "https://sso.example.com", "root", oidcProfile{}); err != nil || linked != "root" {
		t.Fatalf("link to existing user = %q, %v", linked, err)
	}
	if root, err := p.GetUser(ctx, "root"); err != nil || len(root.Identities) != 2 || !root.HasPassword() {
		t.Fatalf("linked user = %+v, %v", root, err)
	}
	if _, err := p.UpdateUser(ctx, "sub-42", UserUpdate{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.resolveOIDCUser(ctx, "https://sso.example.com", "sub-42", oidcProfile{}); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("disabled SSO user error = %v", err)
	}
	// Without a database the subject is used directly, as before.
	if direct, err := (&Provider{config: Config{Mode: "oauth"}}).resolveOIDCUser(ctx, "https://sso.example.com", "sub-99", oidcProfile{}); err != nil || direct != "sub-99" {
		t.Fatalf("direct subject = %q, %v", direct, err)
	}

	// Deleting a user revokes its API tokens.
	token, issued, err := p.issueAPIToken(ctx, "root", "cli", 0)
	if err != nil || issued.ID == 0 {
		t.Fatal(err)
	}
	if got, err := p.VerifyBearerToken(ctx, token); err != nil || got != "root" {
		t.Fatalf("token before delete = %q, %v", got, err)
	}
	if err := p.DeleteUser(ctx, "root"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.VerifyBearerToken(ctx, token); err == nil {
		t.Fatal("token of a deleted user still works")
	}
	if err := p.DeleteUser(ctx, "root"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("second delete error = %v", err)
	}
	if err := p.SetLocalPassword(ctx, "sub-42", "bob gets a password"); err != nil {
		t.Fatal(err)
	}
	if bob, err := p.GetUser(ctx, "sub-42"); err != nil || !bob.HasPassword() || len(bob.Identities) != 2 {
		t.Fatalf("password added to SSO user = %+v, %v", bob, err)
	}

	// Repeated failures from one client are throttled; a different client is not.
	enabled := false
	if _, err := p.UpdateUser(ctx, "alice", UserUpdate{Disabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < localLoginFailureLimit; i++ {
		if rec := localLogin(t, p, "alice", "nope"); rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "너무 많습니다") {
			t.Fatalf("failure %d status=%d", i, rec.Code)
		}
	}
	if rec := localLogin(t, p, "alice", "a brand new password"); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "너무 많습니다") {
		t.Fatalf("throttled login status=%d body=%s", rec.Code, rec.Body.String())
	}
	other := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(url.Values{"username": {"alice"}, "password": {"a brand new password"}}.Encode()))
	other.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	other.RemoteAddr = "198.51.100.9:2"
	otherRec := httptest.NewRecorder()
	p.HandleLogin(otherRec, other)
	if otherRec.Code != http.StatusSeeOther {
		t.Fatalf("other client status=%d", otherRec.Code)
	}
}

func TestAccountsNeedDatabaseAndHTTPMode(t *testing.T) {
	ctx := context.Background()
	for name, p := range map[string]*Provider{
		"nil":   nil,
		"stdio": {config: Config{Mode: "stdio"}},
		"no-db": {config: Config{Mode: "token", APISecret: testSigningSecret}},
	} {
		if _, err := p.CreateLocalUser(ctx, "alice", "correct horse battery", "", false); !errors.Is(err, ErrAccountsUnavailable) {
			t.Fatalf("%s: create error = %v", name, err)
		}
		if p.LocalLoginAvailable(ctx) || p.IsAdmin(ctx, "alice") || p.HasPassword(ctx, "alice") {
			t.Fatalf("%s: accounts reported without storage", name)
		}
	}
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret, AdminSubjects: "alice"}}
	if !p.IsAdmin(ctx, "alice") {
		t.Fatal("STASH_ADMIN_SUBJECTS must work without a users table")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(url.Values{"username": {"alice"}, "password": {"x"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p.HandleLogin(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="token"`) {
		t.Fatalf("password login without storage status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSubjectListed(t *testing.T) {
	if !subjectListed("user-2", "user-1, user-2") {
		t.Fatal("configured subject should match")
	}
	if subjectListed("user-", "user-1, user-2") || subjectListed("user-1", "") || subjectListed("", "") {
		t.Fatal("subject matching must be exact and never empty")
	}
}
