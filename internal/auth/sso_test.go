package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/secrets"
)

// fakeIssuer serves the discovery document an OIDC client needs. Its issuer
// is its own URL, which keeps go-oidc's issuer check happy.
func fakeIssuer(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer":                 server.URL,
				"authorization_endpoint": server.URL + "/authorize",
				"token_endpoint":         server.URL + "/token",
				"jwks_uri":               server.URL + "/jwks",
				"introspection_endpoint": server.URL + "/introspect",
			})
		case "/jwks":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"keys":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSSOProvidersAreStoredAndLoaded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := openAuthTestSchema(t, ctx, nil)
	keyring, err := secrets.NewKeyring(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	issuer := fakeIssuer(t)
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret, CookieSecure: false}, tokenPool: pool}
	if err := p.ReloadSSO(ctx); err != nil {
		t.Fatal(err)
	}
	if p.browserLoginConfigured() || len(p.SSOOptions()) != 0 {
		t.Fatal("SSO offered without any provider")
	}

	// Without a keyring a secret cannot be stored.
	str := func(v string) *string { return &v }
	in := SSOProviderInput{Slug: str("Dev-IdP"), DisplayName: str("Dev IdP"), Issuer: str(issuer.URL), ClientID: str("stash"), ClientSecret: str("s3cret"), RedirectURL: str("http://127.0.0.1:8080/auth/callback")}
	if _, err := p.CreateSSOProvider(ctx, in); !errors.Is(err, ErrSSOSecretsUnavailable) {
		t.Fatalf("create without keyring error = %v", err)
	}
	p.SetSecrets(keyring)
	if _, err := p.CreateSSOProvider(ctx, SSOProviderInput{Slug: str("x"), Issuer: str(issuer.URL), ClientID: str("stash"), RedirectURL: str("http://127.0.0.1:8080/auth/callback")}); !errors.Is(err, ErrInvalidSSOProvider) {
		t.Fatalf("create without secret error = %v", err)
	}
	if _, err := p.CreateSSOProvider(ctx, SSOProviderInput{Slug: str("x"), Issuer: str("http://idp.example.com"), ClientID: str("stash"), ClientSecret: str("s"), RedirectURL: str("http://127.0.0.1:8080/auth/callback")}); !errors.Is(err, ErrInvalidSSOProvider) {
		t.Fatalf("insecure issuer error = %v", err)
	}
	created, err := p.CreateSSOProvider(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if created.Slug != "dev-idp" || created.Status != "ready" || !created.HasClientSecret || created.Source != "console" {
		t.Fatalf("created = %+v", created)
	}
	if _, err := p.CreateSSOProvider(ctx, in); !errors.Is(err, ErrSSOSlugTaken) {
		t.Fatalf("duplicate slug error = %v", err)
	}
	var sealed string
	if err := pool.QueryRow(ctx, `SELECT client_secret_sealed FROM sso_providers WHERE id = $1`, created.ID).Scan(&sealed); err != nil || !secrets.IsSealed(sealed) || strings.Contains(sealed, "s3cret") {
		t.Fatalf("stored secret = %q, %v", sealed, err)
	}
	options := p.SSOOptions()
	if len(options) != 1 || options[0].Slug != "dev-idp" || options[0].Name != "Dev IdP" {
		t.Fatalf("options = %+v", options)
	}
	rt := p.ssoBySlug("dev-idp")
	if rt == nil || rt.clientSecret != "s3cret" || rt.oauth2Config.Endpoint.AuthURL != issuer.URL+"/authorize" || rt.introspectionEndpoint != issuer.URL+"/introspect" || rt.hmacVerifier == nil {
		t.Fatalf("runtime = %+v", rt)
	}

	// The login page offers it and a login starts at the issuer.
	page := httptest.NewRecorder()
	p.HandleLogin(page, httptest.NewRequest(http.MethodGet, "/auth/login?provider=token", nil))
	if !strings.Contains(page.Body.String(), "sso=dev-idp") {
		t.Fatalf("login page body = %s", page.Body.String())
	}
	start := httptest.NewRecorder()
	p.HandleLogin(start, httptest.NewRequest(http.MethodGet, "/auth/login?sso=dev-idp", nil))
	if start.Code != http.StatusFound || !strings.HasPrefix(start.Header().Get("Location"), issuer.URL+"/authorize?") || !strings.Contains(start.Header().Get("Location"), "state=dev-idp.") {
		t.Fatalf("start status=%d location=%q", start.Code, start.Header().Get("Location"))
	}

	// Updating keeps the secret when none is sent, disabling hides it.
	disabled := false
	updated, err := p.UpdateSSOProvider(ctx, created.ID, SSOProviderInput{DisplayName: str("Renamed"), Enabled: &disabled})
	if err != nil || updated.DisplayName != "Renamed" || updated.Enabled || updated.Status != "disabled" || !updated.HasClientSecret {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	if p.browserLoginConfigured() {
		t.Fatal("disabled provider still offered")
	}
	enabled := true
	if _, err := p.UpdateSSOProvider(ctx, created.ID, SSOProviderInput{Enabled: &enabled, ClientSecret: str("rotated")}); err != nil {
		t.Fatal(err)
	}
	if rt := p.ssoBySlug("dev-idp"); rt == nil || rt.clientSecret != "rotated" || !rt.ready() {
		t.Fatalf("runtime after rotation = %+v", rt)
	}
	if err := p.TestSSOProvider(ctx, created.ID); err != nil {
		t.Fatalf("test provider: %v", err)
	}

	// A provider whose issuer is down is reported, not fatal.
	broken, err := p.CreateSSOProvider(ctx, SSOProviderInput{Slug: str("broken"), Issuer: str("http://127.0.0.1:9/"), ClientID: str("c"), ClientSecret: str("s"), RedirectURL: str("http://127.0.0.1:8080/auth/callback")})
	if err != nil || broken.Status != "error" || broken.Error == "" {
		t.Fatalf("broken provider = %+v, %v", broken, err)
	}
	listed, err := p.ListSSOProviders(ctx)
	if err != nil || len(listed) != 2 || listed[0].Slug != "broken" || listed[0].Status != "error" || listed[1].Status != "ready" {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
	if len(p.SSOOptions()) != 1 {
		t.Fatal("broken provider offered for login")
	}

	// Another process changing the table is picked up by the poll.
	if _, err := pool.Exec(ctx, `UPDATE sso_providers SET enabled = false, updated_at = clock_timestamp() WHERE slug = 'broken'`); err != nil {
		t.Fatal(err)
	}
	if changed, err := p.ReloadSSOIfChanged(ctx); err != nil || !changed {
		t.Fatalf("reload if changed = %v, %v", changed, err)
	}
	if changed, err := p.ReloadSSOIfChanged(ctx); err != nil || changed {
		t.Fatalf("second reload if changed = %v, %v", changed, err)
	}
	if err := p.DeleteSSOProvider(ctx, broken.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteSSOProvider(ctx, broken.ID); !errors.Is(err, ErrSSOProviderNotFound) {
		t.Fatalf("second delete error = %v", err)
	}
}

func TestEnvironmentSSOIsImportedOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := openAuthTestSchema(t, ctx, nil)
	keyring, err := secrets.NewKeyring(strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	issuer := fakeIssuer(t)
	cfg := Config{Mode: "oauth", APISecret: testSigningSecret, Issuer: issuer.URL, ClientID: "env-client", ClientSecret: "env-secret", RedirectURL: "http://127.0.0.1:8080/auth/callback"}
	p, err := Init(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.SetTokenPool(pool)

	// Without a keyring the environment provider still works, from memory.
	if err := p.ReloadSSO(ctx); err != nil {
		t.Fatal(err)
	}
	if options := p.SSOOptions(); len(options) != 1 || options[0].Slug != "environment" {
		t.Fatalf("environment fallback options = %+v", options)
	}
	if _, _, err := p.ImportEnvironmentSSO(ctx); !errors.Is(err, ErrSSOSecretsUnavailable) {
		t.Fatalf("import without keyring error = %v", err)
	}
	listed, err := p.ListSSOProviders(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != 0 || listed[0].Source != "environment" || listed[0].Status != "ready" {
		t.Fatalf("listed fallback = %+v, %v", listed, err)
	}

	p.SetSecrets(keyring)
	imported, ok, err := p.ImportEnvironmentSSO(ctx)
	if err != nil || !ok || imported.ID == 0 || imported.Source != "environment" || imported.Slug != "127-0-0-1" || !imported.HasClientSecret {
		t.Fatalf("import = %+v, %v, %v", imported, ok, err)
	}
	if _, ok, err := p.ImportEnvironmentSSO(ctx); err != nil || ok {
		t.Fatalf("second import = %v, %v", ok, err)
	}
	// The stored row replaces the in-memory fallback.
	if options := p.SSOOptions(); len(options) != 1 || options[0].Slug != "127-0-0-1" {
		t.Fatalf("options after import = %+v", options)
	}
	if rt := p.ssoBySlug("127-0-0-1"); rt == nil || rt.clientSecret != "env-secret" {
		t.Fatalf("imported runtime = %+v", rt)
	}
	// Disabling the stored row must not resurrect the environment copy.
	disabled := false
	if _, err := p.UpdateSSOProvider(ctx, imported.ID, SSOProviderInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	if p.browserLoginConfigured() {
		t.Fatal("environment provider came back after the stored row was disabled")
	}
}

func TestSSOSlugFromIssuer(t *testing.T) {
	for issuer, want := range map[string]string{
		"https://auth.example.com/application/o/stash/": "auth-example-com",
		"https://login.microsoftonline.com/abc/v2.0":    "login-microsoftonline-com",
		"not a url": "sso",
	} {
		if got := ssoSlugFromIssuer(issuer); got != want {
			t.Fatalf("slug(%q) = %q, want %q", issuer, got, want)
		}
	}
}
