package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

const (
	testSigningSecret = "0123456789abcdef0123456789abcdef"
	testPKCEVerifier  = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"
)

func TestSessionTokenRoundTripAndExpiry(t *testing.T) {
	token, err := generateSessionToken("subject-1", "test-secret", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("generate session: %v", err)
	}
	got, err := parseSessionToken(token, "test-secret")
	if err != nil || got != "subject-1" {
		t.Fatalf("parse session = %q, %v", got, err)
	}

	payload := strings.Join([]string{
		"c3ViamVjdC0x",
		strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10),
	}, ".")
	expired := sessionTokenPrefix + payload + "." + sign(payload, "test-secret")
	if _, err := parseSessionToken(expired, "test-secret"); err == nil {
		t.Fatal("expired session was accepted")
	}

	provider := &Provider{config: Config{APISecret: "test-secret"}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	if got, err := provider.VerifyRequest(req); err != nil || got != "subject-1" {
		t.Fatalf("VerifyRequest session = %q, %v", got, err)
	}
}

func TestTokenEndpointRequiresPost(t *testing.T) {
	p := &Provider{config: Config{APISecret: "test-secret", APITokenTTL: time.Hour}}
	req := httptest.NewRequest(http.MethodGet, "/auth/token", nil)
	rec := httptest.NewRecorder()
	p.HandleGenerateToken(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestTokenLoginRejectsInvalidTokenAndRendersForm(t *testing.T) {
	p := &Provider{config: Config{Mode: "token", APISecret: "test-secret"}}
	get := httptest.NewRecorder()
	p.HandleLogin(get, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `name="token"`) {
		t.Fatalf("token login page status=%d body=%s", get.Code, get.Body.String())
	}
	if !strings.Contains(get.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || get.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("token login framing headers = CSP %q, X-Frame-Options %q", get.Header().Get("Content-Security-Policy"), get.Header().Get("X-Frame-Options"))
	}

	post := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader("token=not-a-token"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p.HandleLogin(post, req)
	if post.Code != http.StatusUnauthorized || !strings.Contains(post.Body.String(), "토큰을 확인하고 다시 시도하세요") {
		t.Fatalf("invalid token login status=%d body=%s", post.Code, post.Body.String())
	}
	if len(post.Result().Cookies()) != 0 {
		t.Fatalf("invalid token login set cookies: %#v", post.Result().Cookies())
	}
}

func TestTokenLoginRejectsCrossOriginForm(t *testing.T) {
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret}}
	token := apiTokenPrefix + "any-value"
	req := httptest.NewRequest(http.MethodPost, "https://stash.example.com/auth/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://attacker.example")
	rec := httptest.NewRecorder()
	p.HandleLogin(rec, req)
	if rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("cross-origin login status=%d cookies=%#v", rec.Code, rec.Result().Cookies())
	}
}

func TestLogoutRequiresSameOriginPost(t *testing.T) {
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret}}

	get := httptest.NewRecorder()
	p.HandleLogout(get, httptest.NewRequest(http.MethodGet, "https://stash.example.com/auth/logout", nil))
	if get.Code != http.StatusMethodNotAllowed || len(get.Result().Cookies()) != 0 {
		t.Fatalf("GET logout status=%d cookies=%#v", get.Code, get.Result().Cookies())
	}

	crossOriginRequest := httptest.NewRequest(http.MethodPost, "https://stash.example.com/auth/logout", nil)
	crossOriginRequest.Header.Set("Origin", "https://attacker.example")
	crossOrigin := httptest.NewRecorder()
	p.HandleLogout(crossOrigin, crossOriginRequest)
	if crossOrigin.Code != http.StatusForbidden || len(crossOrigin.Result().Cookies()) != 0 {
		t.Fatalf("cross-origin logout status=%d cookies=%#v", crossOrigin.Code, crossOrigin.Result().Cookies())
	}

	sameOriginRequest := httptest.NewRequest(http.MethodPost, "https://stash.example.com/auth/logout", nil)
	sameOriginRequest.Header.Set("Origin", "https://stash.example.com")
	sameOrigin := httptest.NewRecorder()
	p.HandleLogout(sameOrigin, sameOriginRequest)
	if sameOrigin.Code != http.StatusNoContent || len(sameOrigin.Result().Cookies()) != 1 || sameOrigin.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("same-origin logout status=%d cookies=%#v", sameOrigin.Code, sameOrigin.Result().Cookies())
	}
}

func TestSigningSecretMustBeAtLeast32Bytes(t *testing.T) {
	if _, err := Init(context.Background(), Config{Mode: "token", APISecret: "short"}); err == nil {
		t.Fatal("token mode accepted a short signing secret")
	}
	if _, err := Init(context.Background(), Config{Mode: "oauth", Issuer: "https://auth.example.com/", APISecret: "short"}); err == nil {
		t.Fatal("oauth mode accepted a short signing secret")
	}
}

func TestOAuthConfigurationFailsBeforeProviderDiscovery(t *testing.T) {
	base := Config{
		Mode:         "oauth",
		Issuer:       "https://auth.example.com/",
		ClientID:     "browser-client",
		ClientSecret: "browser-secret",
		RedirectURL:  "https://stash.example.com/auth/callback",
		CookieSecure: true,
		APISecret:    testSigningSecret,
	}
	// A complete environment provider is accepted without contacting the
	// issuer; discovery happens later in ReloadSSO.
	if p, err := Init(context.Background(), base); err != nil || p == nil || p.Mode() != "oidc" && p.Mode() != "oauth" {
		t.Fatalf("complete SSO config = %v, %v", p, err)
	}
	// Without any SSO variables the profile is plain token login.
	if p, err := Init(context.Background(), Config{Mode: "oauth", APISecret: testSigningSecret}); err != nil || p == nil || p.browserLoginConfigured() {
		t.Fatalf("SSO-less oauth mode = %v, %v", p, err)
	}
	for name, mutate := range map[string]func(*Config){
		"weak secret":       func(c *Config) { c.APISecret = "short" },
		"half client":       func(c *Config) { c.ClientSecret = "" },
		"missing redirect":  func(c *Config) { c.RedirectURL = "" },
		"insecure issuer":   func(c *Config) { c.Issuer = "http://auth.example.com/" },
		"insecure redirect": func(c *Config) { c.RedirectURL = "http://stash.example.com/auth/callback" },
		"insecure cookie":   func(c *Config) { c.CookieSecure = false },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := Init(context.Background(), cfg); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestOAuthLoginDefaultsToBrowserRedirect(t *testing.T) {
	p, rt := newLocalOAuthProvider()
	rec := httptest.NewRecorder()
	p.HandleLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("OAuth login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, rt.oauth2Config.Endpoint.AuthURL) || !strings.Contains(location, "client_id=authentik-stash") || !strings.Contains(location, "state=authentik.") {
		t.Fatalf("OAuth login location = %q", location)
	}
	var stateCookie *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == stateCookieName {
			stateCookie = cookie
		}
	}
	if stateCookie == nil || !strings.HasPrefix(stateCookie.Value, "authentik.") {
		t.Fatalf("state cookie = %#v", stateCookie)
	}
	// Naming the provider works too; an unknown one does not start a login.
	named := httptest.NewRecorder()
	p.HandleLogin(named, httptest.NewRequest(http.MethodGet, "/auth/login?sso=authentik", nil))
	if named.Code != http.StatusFound {
		t.Fatalf("named SSO login status = %d", named.Code)
	}
	unknown := httptest.NewRecorder()
	p.HandleLogin(unknown, httptest.NewRequest(http.MethodGet, "/auth/login?sso=nope", nil))
	if unknown.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown SSO login status = %d", unknown.Code)
	}
	// With two providers the page lets the person choose.
	second := &ssoRuntime{SSOProvider: SSOProvider{Slug: "okta", DisplayName: "Okta", Enabled: true}, oauth2Config: oauth2.Config{ClientID: "okta-stash", Endpoint: oauth2.Endpoint{AuthURL: "https://okta.example.com/authorize"}}, verifier: &oidc.IDTokenVerifier{}}
	p.installSSO(rt, second)
	choose := httptest.NewRecorder()
	p.HandleLogin(choose, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if choose.Code != http.StatusOK || !strings.Contains(choose.Body.String(), "sso=authentik") || !strings.Contains(choose.Body.String(), "sso=okta") || !strings.Contains(choose.Body.String(), "Okta") {
		t.Fatalf("two-provider login page status=%d body=%s", choose.Code, choose.Body.String())
	}
	status := httptest.NewRecorder()
	p.HandleStatus(status, httptest.NewRequest(http.MethodGet, "/auth/status", nil))
	if !strings.Contains(status.Body.String(), `"sso_login":true`) || !strings.Contains(status.Body.String(), `"slug":"okta"`) {
		t.Fatalf("status = %s", status.Body.String())
	}
}

func TestOAuthLoginCanUseTokenFormExplicitly(t *testing.T) {
	p, _ := newLocalOAuthProvider()
	rec := httptest.NewRecorder()
	p.HandleLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login?provider=token", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="token"`) || !strings.Contains(rec.Body.String(), `sso=authentik`) {
		t.Fatalf("explicit token login status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGenerateTokenNeedsStorage(t *testing.T) {
	p := &Provider{config: Config{APISecret: "test-secret", APITokenTTL: 2 * time.Hour}}
	session, err := generateSessionToken("subject-1", "test-secret", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("generate session: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader("expires_in=7200"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	rec := httptest.NewRecorder()
	p.HandleGenerateToken(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if user, err := p.VerifyRequest(httptest.NewRequest(http.MethodGet, "/", nil)); err == nil || user != "" {
		t.Fatalf("missing credential unexpectedly verified: %q, %v", user, err)
	}
	verify := httptest.NewRequest(http.MethodGet, "/", nil)
	verify.Header.Set("Authorization", "Bearer "+apiTokenPrefix+"not-stored")
	if user, err := p.VerifyRequest(verify); err == nil || user != "" {
		t.Fatalf("token verified without storage: %q, %v", user, err)
	}
}

func TestHandleGenerateTokenLifetimeValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openAuthTestSchema(t, ctx, nil)
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret, APITokenTTL: 2 * time.Hour}, tokenPool: pool}
	session, err := generateSessionToken("subject-1", testSigningSecret, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		form   string
		status int
	}{
		{"", http.StatusOK},
		{"expires_in=3600", http.StatusOK},
		{"expires_in=9223372036", http.StatusOK},
		{"expires_in=0", http.StatusOK},
		{"expires_in=", http.StatusOK},
		{"expires_in=++", http.StatusOK}, // form-decoded whitespace means "no expiry"
		{"expires_in=-1", http.StatusBadRequest},
		{"expires_in=1.5", http.StatusBadRequest},
		{"expires_in=abc", http.StatusBadRequest},
		{"expires_in=9223372037", http.StatusBadRequest},
		{"expires_in=9223372036854775808", http.StatusBadRequest},
		{"expires_in=0&expires_in=3600", http.StatusBadRequest},
		{"expires_in=&expires_in=3600", http.StatusBadRequest},
	} {
		t.Run(tt.form, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(tt.form))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			rec := httptest.NewRecorder()
			p.HandleGenerateToken(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if tt.status != http.StatusOK {
				return
			}
			var issued struct {
				ID        int64      `json:"id"`
				Token     string     `json:"token"`
				ExpiresIn int64      `json:"expires_in"`
				ExpiresAt *time.Time `json:"expires_at"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&issued); err != nil {
				t.Fatal(err)
			}
			seconds, _ := strconv.ParseInt(req.PostFormValue("expires_in"), 10, 64)
			if issued.ID == 0 || !strings.HasPrefix(issued.Token, apiTokenPrefix) || issued.ExpiresIn != seconds || (issued.ExpiresAt == nil) != (seconds == 0) {
				t.Fatalf("issued = %+v for %q", issued, tt.form)
			}
			if got, expiry, err := p.verifyAPIToken(ctx, issued.Token); err != nil || got != "subject-1" || (seconds == 0) != expiry.IsZero() {
				t.Fatalf("stored token = %q expiry=%v err=%v", got, expiry, err)
			}
		})
	}
}

func TestMCPAcceptsOnlyStoredAPITokens(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openAuthTestSchema(t, ctx, nil)
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret}, tokenPool: pool}
	token, metadata, err := p.IssueAPIToken(ctx, "codex", "laptop", 0)
	if err != nil || metadata.ID == 0 || metadata.ExpiresAt != nil {
		t.Fatalf("issue token: %+v, %v", metadata, err)
	}
	if _, _, err := p.IssueAPIToken(ctx, "  ", "", 0); err == nil {
		t.Fatal("token issued without a subject")
	}
	session, err := generateSessionToken("codex", testSigningSecret, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	bearer := func(value string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+value)
		return req
	}
	if got, err := p.VerifyMCPRequest(bearer(token)); err != nil || got != "codex" {
		t.Fatalf("stored token over MCP = %q, %v", got, err)
	}
	if got, err := p.VerifyBearerToken(ctx, token); err != nil || got != "codex" {
		t.Fatalf("stored token over STDIO = %q, %v", got, err)
	}
	for name, value := range map[string]string{
		"session as bearer": session,
		"unknown api token": apiTokenPrefix + "never-issued",
		"opaque upstream":   "eyJhbGciOiJSUzI1NiJ9.upstream.access",
		"old oauth prefix":  "stash_oauth_" + "anything",
	} {
		if got, err := p.VerifyMCPRequest(bearer(value)); err == nil || got != "" {
			t.Fatalf("%s accepted over MCP: %q", name, got)
		}
		if got, err := p.VerifyBearerToken(ctx, value); err == nil || got != "" {
			t.Fatalf("%s accepted over STDIO: %q", name, got)
		}
	}
	// The console keeps using its session cookie on the same origin.
	console := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	console.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	if got, err := p.VerifyMCPRequest(console); err != nil || got != "codex" {
		t.Fatalf("session cookie over MCP = %q, %v", got, err)
	}
	// Revocation takes effect immediately, including over STDIO.
	if _, err := p.revokeAPIToken(ctx, "codex", metadata.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.VerifyMCPRequest(bearer(token)); err == nil {
		t.Fatal("revoked token accepted over MCP")
	}
	if _, err := p.VerifyBearerToken(ctx, token); err == nil {
		t.Fatal("revoked token accepted over STDIO")
	}
}

// authTestMigrations are the auth-only migrations, which need PostgreSQL but
// not pgvector, so these checks run in a plain isolated schema.
var authTestMigrations = []string{"00040_add_auth_tokens.sql", "00041_add_auth_token_expiry.sql", "00045_add_users.sql", "00047_add_sso_providers.sql"}

// openAuthTestSchema returns a pool whose search_path is a fresh schema with
// the auth migrations applied. afterMigration runs after each migration so a
// test can insert rows that predate a later one.
func openAuthTestSchema(t *testing.T, ctx context.Context, afterMigration func(pool *pgxpool.Pool, migration string)) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("STASH_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("set STASH_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database URL")
	}
	schema := fmt.Sprintf("auth_test_%d", time.Now().UnixNano())
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	for _, migration := range authTestMigrations {
		sql, err := os.ReadFile("../db/migrations/" + migration)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(sql), "-- +goose Down")
		if _, err := pool.Exec(ctx, up); err != nil {
			t.Fatalf("%s: %v", migration, err)
		}
		if afterMigration != nil {
			afterMigration(pool, migration)
		}
	}
	return pool
}

func TestPersistentAPITokenLifetimeAndRevocation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	legacyToken := apiTokenPrefix + "legacy-test-token"
	pool := openAuthTestSchema(t, ctx, func(pool *pgxpool.Pool, migration string) {
		if migration == "00040_add_auth_tokens.sql" {
			hash := sha256.Sum256([]byte(legacyToken))
			if _, err := pool.Exec(ctx, `INSERT INTO auth_tokens (subject, token_hash) VALUES ('legacy', $1)`, hash[:]); err != nil {
				t.Fatal(err)
			}
		}
	})
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret}, tokenPool: pool}
	if subject, expiry, err := p.verifyAPIToken(ctx, legacyToken); err != nil || subject != "legacy" || !expiry.IsZero() {
		t.Fatalf("legacy token after migration: subject=%q expiry=%v error=%v", subject, expiry, err)
	}
	for _, tt := range []struct{ name, seconds string }{
		{"default", ""}, {"empty", ""}, {"blank", " \t "}, {"unlimited", "0"}, {"one-day", "86400"}, {"manual", "604800"}, {"custom-days", "3888000"}, {"maximum", "9223372036"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			subject := "test-" + tt.name
			session, err := generateSessionToken(subject, testSigningSecret, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			form := url.Values{"name": {"test token"}}
			if tt.name != "default" {
				form.Set("expires_in", tt.seconds)
			}
			before := time.Now()
			issueRequest := httptest.NewRequest(http.MethodPost, "/auth/token", strings.NewReader(form.Encode()))
			issueRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			issueRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			issueResponse := httptest.NewRecorder()
			p.HandleGenerateToken(issueResponse, issueRequest)
			if issueResponse.Code != http.StatusOK {
				t.Fatalf("issue status = %d", issueResponse.Code)
			}
			var issued struct {
				APIToken
				Token     string `json:"token"`
				ExpiresIn int64  `json:"expires_in"`
			}
			if err := json.NewDecoder(issueResponse.Body).Decode(&issued); err != nil {
				t.Fatal(err)
			}
			seconds, _ := strconv.ParseInt(tt.seconds, 10, 64)
			if issued.Token == "" || issued.ID == 0 || issued.ExpiresIn != seconds || (issued.ExpiresAt == nil) != (seconds == 0) {
				t.Fatalf("unexpected issue metadata: id=%d expires_in=%d expires_at=%v", issued.ID, issued.ExpiresIn, issued.ExpiresAt)
			}
			if seconds > 0 {
				ttl := time.Duration(seconds) * time.Second
				if issued.ExpiresAt.Before(before.Add(ttl).Add(-time.Second)) || issued.ExpiresAt.After(time.Now().Add(ttl)) {
					t.Fatal("stored expiry does not match the requested lifetime")
				}
			}
			listRequest := httptest.NewRequest(http.MethodGet, "/auth/tokens", nil)
			listRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			listResponse := httptest.NewRecorder()
			p.HandleTokens(listResponse, listRequest)
			if listResponse.Code != http.StatusOK || strings.Contains(listResponse.Body.String(), issued.Token) {
				t.Fatalf("token list failed or exposed raw token: status=%d", listResponse.Code)
			}
			var listed struct {
				Tokens []APIToken `json:"tokens"`
			}
			if err := json.NewDecoder(listResponse.Body).Decode(&listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Tokens) != 1 || listed.Tokens[0].ID != issued.ID || listed.Tokens[0].Name != "test token" {
				t.Fatal("token list does not match the issued token")
			}
			if (listed.Tokens[0].ExpiresAt == nil) != (seconds == 0) || seconds > 0 && !listed.Tokens[0].ExpiresAt.Equal(*issued.ExpiresAt) {
				t.Fatal("token list lost the stored expiry")
			}
			if got, err := p.VerifyBearerToken(ctx, issued.Token); err != nil || got != subject {
				t.Fatalf("verify persistent token = %q, %v", got, err)
			}
			login := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(url.Values{"token": {issued.Token}}.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				p.HandleLogin(rec, req)
				return rec
			}
			loginResponse := login()
			cookies := loginResponse.Result().Cookies()
			if loginResponse.Code != http.StatusSeeOther || len(cookies) != 1 {
				t.Fatalf("token login status = %d", loginResponse.Code)
			}
			if seconds > 0 && !cookies[0].Expires.Equal(issued.ExpiresAt.Truncate(time.Second)) {
				t.Fatal("session outlives the token")
			}
			if seconds > 0 && tt.name != "manual" {
				if _, err := pool.Exec(ctx, `UPDATE auth_tokens SET expires_at = clock_timestamp() WHERE id = $1`, issued.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := p.VerifyBearerToken(ctx, issued.Token); err == nil {
					t.Fatal("expired persistent token was accepted by the bearer verifier")
				}
				request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
				request.Header.Set("Authorization", "Bearer "+issued.Token)
				for _, verify := range []func(*http.Request) (string, error){p.VerifyRequest, p.VerifyMCPRequest} {
					if _, err := verify(request); err == nil {
						t.Fatal("expired persistent token was accepted over HTTP")
					}
				}
				if rec := login(); rec.Code != http.StatusUnauthorized || len(rec.Result().Cookies()) != 0 {
					t.Fatal("expired persistent token created a login session")
				}
			}
			request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/auth/tokens/%d/revoke", issued.ID), nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
			response := httptest.NewRecorder()
			revokeStarted := time.Now()
			p.HandleRevokeToken(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("revoke status = %d", response.Code)
			}
			if _, err := p.VerifyBearerToken(ctx, issued.Token); err == nil {
				t.Fatal("revoked persistent token was accepted")
			}
			var revoked struct {
				RevokedAt time.Time `json:"revoked_at"`
				ExpiresAt time.Time `json:"expires_at"`
			}
			if err := json.NewDecoder(response.Body).Decode(&revoked); err != nil {
				t.Fatal(err)
			}
			if !revoked.ExpiresAt.Equal(revoked.RevokedAt) || revoked.ExpiresAt.Before(revokeStarted.Add(-time.Second)) || revoked.ExpiresAt.After(time.Now()) {
				t.Fatal("revocation did not return the immediate expiry")
			}
			tokens, err := p.listAPITokens(ctx, subject)
			if err != nil || len(tokens) != 1 || tokens[0].ExpiresAt == nil || !tokens[0].ExpiresAt.Equal(revoked.ExpiresAt) || tokens[0].RevokedAt == nil || !tokens[0].RevokedAt.Equal(revoked.RevokedAt) {
				t.Fatal("token list does not contain the updated expiry")
			}
			repeatedAt, err := p.revokeAPIToken(ctx, subject, issued.ID)
			if err != nil || !repeatedAt.Equal(revoked.RevokedAt) {
				t.Fatal("repeating revocation changed its original time")
			}
		})
	}
}

func TestHandleStatusReportsConfiguredAuthMode(t *testing.T) {
	for _, test := range []struct {
		name string
		p    *Provider
		mode string
	}{
		{name: "disabled", p: nil, mode: "none"},
		{name: "oidc", p: &Provider{config: Config{Mode: "oidc"}}, mode: "oidc"},
		{name: "oauth", p: &Provider{config: Config{Mode: "oauth"}}, mode: "oauth"},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
			rec := httptest.NewRecorder()
			test.p.HandleStatus(rec, req)
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
			}

			var body struct {
				AuthMode      string `json:"auth_mode"`
				Authenticated bool   `json:"authenticated"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode status: %v", err)
			}
			if body.AuthMode != test.mode {
				t.Fatalf("auth_mode = %q, want %q", body.AuthMode, test.mode)
			}
			if body.Authenticated {
				t.Fatal("unauthenticated status reported an authenticated user")
			}
		})
	}
}

// newLocalOAuthProvider returns a provider with one ready SSO runtime
// installed without discovery.
func newLocalOAuthProvider() (*Provider, *ssoRuntime) {
	p := &Provider{config: Config{Mode: "oauth", APISecret: "test-secret", APITokenTTL: time.Hour}}
	rt := &ssoRuntime{
		SSOProvider:  SSOProvider{Slug: "authentik", DisplayName: "Authentik", Issuer: "https://auth.example.com/", ClientID: "authentik-stash", Enabled: true},
		oauth2Config: oauth2.Config{ClientID: "authentik-stash", Endpoint: oauth2.Endpoint{AuthURL: "https://auth.example.com/application/o/authorize/"}},
		verifier:     &oidc.IDTokenVerifier{},
	}
	p.installSSO(rt)
	return p, rt
}

func TestHMACOIDCVerifierAcceptsAuthentikStyleIDToken(t *testing.T) {
	issuer := "https://auth.example.com/application/o/stash/"
	clientID := "stash-browser"
	secret := strings.Repeat("s", 64)
	now := time.Now().UTC().Truncate(time.Second)
	verifier := newHMACVerifier(issuer, clientID, secret, false)
	if verifier == nil {
		t.Fatal("HMAC verifier was not created")
	}
	for _, algorithm := range hmacSigningAlgorithms {
		t.Run(string(algorithm), func(t *testing.T) {
			signer, err := jose.NewSigner(jose.SigningKey{Algorithm: algorithm, Key: []byte(secret)}, nil)
			if err != nil {
				t.Fatalf("create HMAC signer: %v", err)
			}
			rawToken, err := jwt.Signed(signer).
				Claims(jwt.Claims{
					Issuer:   issuer,
					Subject:  "subject-1",
					Audience: jwt.Audience{clientID},
					Expiry:   jwt.NewNumericDate(now.Add(time.Hour)),
					IssuedAt: jwt.NewNumericDate(now),
				}).
				Claims(map[string]interface{}{"nonce": "nonce-1"}).
				Serialize()
			if err != nil {
				t.Fatalf("sign HMAC ID token: %v", err)
			}
			idToken, err := verifier.Verify(context.Background(), rawToken)
			if err != nil {
				t.Fatalf("verify HMAC ID token: %v", err)
			}
			if idToken.Subject != "subject-1" || idToken.Nonce != "nonce-1" {
				t.Fatalf("verified token = subject %q, nonce %q", idToken.Subject, idToken.Nonce)
			}
			wrongSecretVerifier := newHMACVerifier(issuer, clientID, strings.Repeat("x", 64), false)
			if _, err := wrongSecretVerifier.Verify(context.Background(), rawToken); err == nil {
				t.Fatal("HMAC token verified with the wrong client secret")
			}
		})
	}
}

func TestLoginAccessTokenIntrospectionRequiresBrowserAudience(t *testing.T) {
	expires := time.Now().Add(time.Hour).Unix()
	audience := []string{"browser-client"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, secret, ok := r.BasicAuth(); !ok || user != "browser-client" || secret != "client-secret" {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"active": true,
			"sub":    "subject-1",
			"exp":    expires,
			"aud":    audience,
		})
	}))
	defer server.Close()

	p := &Provider{}
	rt := &ssoRuntime{SSOProvider: SSOProvider{Slug: "sso", ClientID: "browser-client"}, clientSecret: "client-secret", introspectionEndpoint: server.URL}
	subject, gotExpiry, err := p.introspectLoginAccessToken(rt, context.Background(), "access-token")
	if err != nil {
		t.Fatalf("introspect login token: %v", err)
	}
	if subject != "subject-1" || gotExpiry.Unix() != expires {
		t.Fatalf("introspected identity = (%q, %v), want subject and expiry", subject, gotExpiry)
	}

	audience = []string{"mcp-client"}
	if _, _, err := p.introspectLoginAccessToken(rt, context.Background(), "access-token"); err == nil || !strings.Contains(err.Error(), "unexpected audience") {
		t.Fatalf("unexpected audience result = %v", err)
	}
}

func TestCompleteOIDCLoginFallsBackToIntrospection(t *testing.T) {
	expires := time.Now().Add(time.Hour).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "access-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
				"id_token":     "not-a-valid-id-token",
			})
		case "/introspect":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"active": true,
				"sub":    "subject-1",
				"exp":    expires,
				"aud":    []string{"browser-client"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	verifier := newHMACVerifier("https://auth.example.com/", "browser-client", "wrong-secret", false)
	p := &Provider{}
	rt := &ssoRuntime{
		SSOProvider:  SSOProvider{Slug: "sso", Issuer: "https://auth.example.com/", ClientID: "browser-client", RedirectURL: "https://stash.example.com/auth/callback", Enabled: true},
		clientSecret: "client-secret",
		oauth2Config: oauth2.Config{
			ClientID:     "browser-client",
			ClientSecret: "client-secret",
			RedirectURL:  "https://stash.example.com/auth/callback",
			Endpoint: oauth2.Endpoint{
				TokenURL: server.URL + "/token",
			},
		},
		verifier:              verifier,
		hmacVerifier:          verifier,
		introspectionEndpoint: server.URL + "/introspect",
	}
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/auth/callback?state=sso.internal-state&code=auth-code", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "sso.internal-state"})
	req.AddCookie(&http.Cookie{Name: nonceCookieName, Value: "nonce"})
	rec := httptest.NewRecorder()
	subject, gotExpiry, authErr := p.completeOIDCLogin(rt, req, "sso.internal-state", rec)
	if authErr != nil {
		t.Fatalf("complete login error = %v", authErr)
	}
	// The session lifetime is local, not the upstream token's short expiry.
	if subject.Subject != "subject-1" || gotExpiry.Unix() == expires || gotExpiry.Before(time.Now().Add(defaultSessionTTL-time.Minute)) {
		t.Fatalf("completed identity = (%q, %v), want subject and local session expiry", subject.Subject, gotExpiry)
	}
}

func TestRenewSessionSlidesOnlyRenewableSessions(t *testing.T) {
	p := &Provider{config: Config{APISecret: "test-secret", SessionTTL: 10 * time.Hour}}
	renew := func(token string, bearer bool) *http.Cookie {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/auth/status", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		if bearer {
			req.Header.Set("Authorization", "Bearer something")
		}
		rec := httptest.NewRecorder()
		p.RenewSession(rec, req)
		cookies := rec.Result().Cookies()
		if len(cookies) == 0 {
			return nil
		}
		return cookies[0]
	}

	fresh, _ := signSessionToken("subject-1", "test-secret", time.Now().Add(9*time.Hour), true)
	if renew(fresh, false) != nil {
		t.Fatal("session with more than half its lifetime left was reissued")
	}
	aging, _ := signSessionToken("subject-1", "test-secret", time.Now().Add(time.Hour), true)
	cookie := renew(aging, false)
	if cookie == nil {
		t.Fatal("aging renewable session was not reissued")
	}
	subject, expiresAt, renewable, err := parseSessionClaims(cookie.Value, "test-secret")
	if err != nil || subject != "subject-1" || !renewable || expiresAt.Before(time.Now().Add(10*time.Hour-time.Minute)) {
		t.Fatalf("renewed session = %q %v %v %v", subject, expiresAt, renewable, err)
	}
	if renew(aging, true) != nil {
		t.Fatal("bearer request reissued a session cookie")
	}
	fixed, _ := generateSessionToken("subject-1", "test-secret", time.Now().Add(time.Hour))
	if renew(fixed, false) != nil {
		t.Fatal("non-renewable session was extended")
	}
	if got, err := parseSessionToken(fixed, "test-secret"); err != nil || got != "subject-1" {
		t.Fatalf("legacy session parse = %q, %v", got, err)
	}
	tampered := strings.Replace(fixed, sessionTokenPrefix, sessionTokenPrefix, 1)
	parts := strings.Split(tampered, ".")
	forged := strings.Join(append(parts[:2], "r", parts[2]), ".")
	if _, err := parseSessionToken(forged, "test-secret"); err == nil {
		t.Fatal("renewable flag was accepted without a matching signature")
	}
}

func TestStdioModeDoesNotExposeHTTPAuthentication(t *testing.T) {
	p, err := Init(context.Background(), Config{Mode: "stdio"})
	if err != nil {
		t.Fatalf("init stdio auth: %v", err)
	}
	if p == nil || p.Mode() != "stdio" || p.HTTPAuthEnabled() {
		t.Fatalf("stdio provider = %#v, HTTPAuthEnabled = %v", p, p.HTTPAuthEnabled())
	}
	if _, err := p.VerifyRequest(httptest.NewRequest(http.MethodGet, "/mcp", nil)); err == nil {
		t.Fatal("stdio provider accepted an HTTP request")
	}
}

func TestTokenModeDoesNotContactOIDC(t *testing.T) {
	p, err := Init(context.Background(), Config{Mode: "token", APISecret: testSigningSecret, APITokenTTL: time.Hour})
	if err != nil {
		t.Fatalf("init token auth: %v", err)
	}
	if p == nil || p.Mode() != "token" || !p.HTTPAuthEnabled() {
		t.Fatalf("token provider = %#v, HTTPAuthEnabled = %v", p, p.HTTPAuthEnabled())
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer opaque-upstream-access-token")
	if got, err := p.VerifyMCPRequest(req); err == nil || got != "" {
		t.Fatalf("token mode accepted a foreign bearer token: %q, %v", got, err)
	}
}

func TestMCPUnauthorizedChallengesWithBearer(t *testing.T) {
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret}}
	rec := httptest.NewRecorder()
	p.MCPUnauthorized(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Bearer ") || strings.Contains(rec.Header().Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("challenge status=%d header=%q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestBearerTokenAcceptsHTTPWhitespace(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "\tBearer   token-value  ")
	if got := bearerToken(req); got != "token-value" {
		t.Fatalf("bearer token = %q, want token-value", got)
	}
}

func TestVerifyRequestDoesNotTreatAnIDTokenCookieAsAnMCPCredential(t *testing.T) {
	p := &Provider{config: Config{APISecret: "test-secret"}}
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "eyJ.fake.id-token"})
	if _, err := p.VerifyRequest(req); err == nil || !strings.Contains(err.Error(), "unsupported credential") {
		t.Fatalf("ID-token-shaped cookie was accepted or returned the wrong error: %v", err)
	}
}
