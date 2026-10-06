package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

const (
	stateCookieName      = "oauthstate"
	nonceCookieName      = "oidc_nonce"
	sessionCookieName    = "stash_session"
	apiTokenPrefix       = "stash_api_"
	sessionTokenPrefix   = "stash_session_"
	defaultTokenTTL      = 30 * 24 * time.Hour
	defaultSessionTTL    = 30 * 24 * time.Hour
	loginStateTTL        = 10 * time.Minute
	oauthProviderTimeout = 15 * time.Second
	minimumSecretBytes   = 32
)

// Config contains the settings needed by the HTTP authentication boundary.
type Config struct {
	Mode         string
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	APISecret    string
	CookieSecure bool
	APITokenTTL  time.Duration
	// SessionTTL is the browser console session lifetime after a login. It is
	// independent of the upstream ID-token expiry, which is usually only
	// minutes long and would otherwise sign the user out almost immediately.
	SessionTTL time.Duration
	StdioToken string
	// AdminSubjects lists OIDC or token subjects allowed on the operator
	// pages, next to local administrators.
	AdminSubjects string
}

type Provider struct {
	config                Config
	tokenPool             *pgxpool.Pool
	oidcProvider          *oidc.Provider
	oauth2Config          oauth2.Config
	verifier              *oidc.IDTokenVerifier
	hmacVerifier          *oidc.IDTokenVerifier
	introspectionEndpoint string
	mu                    sync.Mutex
	loginFailures         map[string]*loginFailure
}

// APIToken is the metadata shown in the token management page. The raw token
// is intentionally absent; it is returned only once from HandleGenerateToken.
type APIToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// hmacKeySet adapts a configured OIDC client secret to go-oidc's KeySet
// interface. go-oidc intentionally leaves HS256 out of provider discovery;
// Authentik uses it when its provider has no asymmetric signing key.
type hmacKeySet struct {
	key []byte
}

var hmacSigningAlgorithms = []jose.SignatureAlgorithm{
	jose.HS256,
	jose.HS384,
	jose.HS512,
}

func (s hmacKeySet) VerifySignature(_ context.Context, rawJWT string) ([]byte, error) {
	jws, err := jose.ParseSigned(rawJWT, hmacSigningAlgorithms)
	if err != nil {
		return nil, err
	}
	return jws.Verify(s.key)
}

func newHMACVerifier(issuer, clientID, secret string, skipClientIDCheck bool) *oidc.IDTokenVerifier {
	issuer = strings.TrimSpace(issuer)
	if issuer == "" || strings.TrimSpace(secret) == "" {
		return nil
	}
	config := &oidc.Config{
		SupportedSigningAlgs: []string{string(jose.HS256), string(jose.HS384), string(jose.HS512)},
		SkipClientIDCheck:    skipClientIDCheck,
	}
	if !skipClientIDCheck {
		clientID = strings.TrimSpace(clientID)
		if clientID == "" {
			return nil
		}
		config.ClientID = clientID
	}
	return oidc.NewVerifier(issuer, hmacKeySet{key: []byte(secret)}, config)
}

var browserRequestProtection = http.NewCrossOriginProtection()

// Init returns nil when authentication is explicitly disabled.
func Init(ctx context.Context, cfg Config) (*Provider, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	cfg.Issuer = strings.TrimSpace(cfg.Issuer)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.RedirectURL = strings.TrimSpace(cfg.RedirectURL)
	if mode == "" || mode == "none" {
		log.Println("Auth mode: none (HTTP authentication is disabled)")
		return nil, nil
	}
	if mode == "oidc" {
		// Keep the old name as a configuration alias. HTTP MCP still uses the
		// OAuth 2.1 resource-server behavior below.
		mode = "oauth"
		cfg.Mode = "oidc"
	}
	if mode == "stdio" {
		log.Println("Auth mode: stdio (credentials come from the process environment)")
		return &Provider{config: cfg}, nil
	}
	if mode == "token" {
		if err := validateSigningSecret(cfg.APISecret); err != nil {
			return nil, fmt.Errorf("token mode requires STASH_AUTH_API_SECRET: %w", err)
		}
		if cfg.APITokenTTL <= 0 {
			cfg.APITokenTTL = defaultTokenTTL
		}
		log.Println("Auth mode: token (Stash API tokens; OIDC is not used)")
		return &Provider{config: cfg}, nil
	}
	if mode != "oauth" {
		return nil, fmt.Errorf("unsupported auth mode: %q", cfg.Mode)
	}
	if cfg.Issuer == "" {
		return nil, errors.New("OAuth mode requires an issuer")
	}
	if !validResourceURL(cfg.Issuer) {
		return nil, errors.New("OAuth issuer must use HTTPS or loopback HTTP")
	}
	if err := validateSigningSecret(cfg.APISecret); err != nil {
		return nil, fmt.Errorf("HTTP authentication requires STASH_AUTH_API_SECRET: %w", err)
	}
	// The browser login is optional: MCP clients authenticate with API tokens,
	// so a deployment may run with SSO only for the console. A client
	// ID/secret pair may be supplied without a redirect URI for opaque-token
	// introspection only. Once a redirect URI is supplied, require the
	// complete browser-login set so a half-configured /auth/login path cannot
	// fail later with a cryptic error.
	if (cfg.ClientID == "") != (cfg.ClientSecret == "") {
		return nil, errors.New("OAuth client ID and client secret must be supplied together")
	}
	webLoginConfigured := cfg.RedirectURL != ""
	if webLoginConfigured && (cfg.ClientID == "" || cfg.ClientSecret == "") {
		return nil, errors.New("browser OAuth login requires client ID, client secret, and redirect URL together")
	}
	if webLoginConfigured && !validRedirectURI(cfg.RedirectURL) {
		return nil, errors.New("browser OAuth redirect URL must use HTTPS or loopback HTTP")
	}
	if webLoginConfigured {
		redirect, _ := url.Parse(cfg.RedirectURL)
		if redirect.Scheme == "https" && !cfg.CookieSecure {
			return nil, errors.New("browser OAuth over HTTPS requires secure cookies")
		}
		if redirect.Scheme == "http" && cfg.CookieSecure {
			return nil, errors.New("loopback HTTP browser OAuth requires STASH_AUTH_COOKIE_SECURE=false")
		}
	}
	if webLoginConfigured && cfg.APISecret == "" {
		return nil, errors.New("browser OAuth login requires STASH_AUTH_API_SECRET for the session signer")
	}
	if cfg.APITokenTTL <= 0 {
		cfg.APITokenTTL = defaultTokenTTL
	}
	discoveryCtx, discoveryCancel := context.WithTimeout(ctx, oauthProviderTimeout)
	defer discoveryCancel()
	provider, err := oidc.NewProvider(discoveryCtx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("initialize OIDC provider: %w", err)
	}

	var oauth2Config oauth2.Config
	var verifier *oidc.IDTokenVerifier
	if webLoginConfigured {
		endpoint := provider.Endpoint()
		if !validRedirectURI(endpoint.AuthURL) || !validRedirectURI(endpoint.TokenURL) {
			return nil, errors.New("OIDC provider returned an unsafe authorization or token endpoint")
		}
		oauth2Config = oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     endpoint,
			Scopes:       []string{oidc.ScopeOpenID},
		}
		verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	}
	// Authentik can issue HS256 ID tokens when its provider has no asymmetric
	// signing key. go-oidc intentionally excludes symmetric algorithms from
	// discovery, so verify HS256 separately with the confidential client's
	// secret. This remains bound to the configured issuer and audience through
	// go-oidc's normal claim checks.
	hmacVerifier := newHMACVerifier(cfg.Issuer, cfg.ClientID, cfg.ClientSecret, false)
	introspectionEndpoint := providerIntrospectionEndpoint(provider)
	log.Printf("Auth mode: oauth (console SSO via %s; MCP clients use API tokens)", cfg.Issuer)
	return &Provider{
		config:                cfg,
		oidcProvider:          provider,
		oauth2Config:          oauth2Config,
		verifier:              verifier,
		hmacVerifier:          hmacVerifier,
		introspectionEndpoint: introspectionEndpoint,
	}, nil
}

// SetTokenPool connects durable API-token storage after database migrations
// have run. The provider is fully usable without it for legacy signed tokens.
func (p *Provider) SetTokenPool(pool *pgxpool.Pool) {
	if p != nil {
		p.tokenPool = pool
	}
}

// Mode reports the configured authentication profile. An OIDC configuration
// is kept as "oidc" for status and compatibility, but its HTTP behavior is
// the OAuth profile required by MCP.
func (p *Provider) Mode() string {
	if p == nil {
		return "none"
	}
	mode := strings.ToLower(strings.TrimSpace(p.config.Mode))
	if mode == "" {
		// A non-nil provider is an authentication boundary. Treat a manually
		// constructed provider with missing configuration as protected so it
		// fails closed in tests and embedding applications.
		return "oauth"
	}
	return mode
}

func (p *Provider) oauthMode() bool {
	return p != nil && (p.Mode() == "oauth" || p.Mode() == "oidc")
}

// HTTPAuthEnabled reports whether the HTTP MCP transports require a bearer
// credential. STDIO credentials never turn into HTTP authentication.
func (p *Provider) HTTPAuthEnabled() bool {
	if p == nil {
		return false
	}
	mode := p.Mode()
	return mode != "none" && mode != "stdio"
}

// StdioCredential returns the optional credential configured for the STDIO
// profile. It is intentionally not exposed in status responses or logs.
func (p *Provider) StdioCredential() string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(p.config.StdioToken)
}

func (p *Provider) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if p == nil {
		http.Error(w, "authentication is disabled", http.StatusNotFound)
		return
	}
	if p.Mode() == "stdio" {
		http.Error(w, "browser login is not available for STDIO authentication", http.StatusNotImplemented)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		if err := r.ParseForm(); err != nil {
			writeTokenLoginPage(w, true, p.browserLoginConfigured())
			return
		}
		if _, ok := r.PostForm["username"]; ok {
			p.handleLocalLogin(w, r)
			return
		}
		p.handleTokenLogin(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if provider != "" && provider != "oidc" && provider != "token" && provider != "local" {
		http.Error(w, "unsupported login provider", http.StatusBadRequest)
		return
	}
	page := loginPageOptions{OAuthEnabled: p.browserLoginConfigured(), LocalEnabled: p.LocalLoginAvailable(r.Context())}
	page.TokenForm = provider == "token" || !page.LocalEnabled
	if p.Mode() == "token" || provider == "token" || provider == "local" || !page.OAuthEnabled || page.LocalEnabled && provider == "" {
		// A deployment with local accounts shows the password form first;
		// SSO stays one link away. Without accounts the token form remains.
		writeLoginPage(w, page)
		return
	}
	if p.verifier == nil || p.oauth2Config.ClientID == "" {
		http.Error(w, "browser login is not configured", http.StatusServiceUnavailable)
		return
	}

	state, nonce, err := setOAuthCookies(w, p.config.CookieSecure)
	if err != nil {
		http.Error(w, "could not start login", http.StatusInternalServerError)
		return
	}

	url := p.oauth2Config.AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", nonce))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, url, http.StatusFound)
}

func (p *Provider) browserLoginConfigured() bool {
	return p != nil && p.oauthMode() && p.verifier != nil && strings.TrimSpace(p.oauth2Config.ClientID) != ""
}

func (p *Provider) handleTokenLogin(w http.ResponseWriter, r *http.Request) {
	if err := browserRequestProtection.Check(r); err != nil {
		http.Error(w, "요청 출처를 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		writeTokenLoginPage(w, true, p.browserLoginConfigured())
		return
	}
	rawToken := strings.TrimSpace(r.PostFormValue("token"))
	subject, expiresAt, err := p.verifyAPIToken(r.Context(), rawToken)
	if err != nil {
		writeTokenLoginPage(w, true, p.browserLoginConfigured())
		return
	}
	// A session backed by an unlimited token slides like an OIDC session; one
	// backed by an expiring token must never outlive that token.
	renewable := expiresAt.IsZero()
	if renewable {
		expiresAt = time.Now().Add(p.sessionTTL())
	}
	p.setSessionCookie(w, subject, expiresAt, renewable)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// loginPageOptions selects which forms the login page shows.
type loginPageOptions struct {
	Failed       bool
	Throttled    bool
	OAuthEnabled bool
	LocalEnabled bool
	TokenForm    bool
}

func writeTokenLoginPage(w http.ResponseWriter, failed, oauthEnabled bool) {
	writeLoginPage(w, loginPageOptions{Failed: failed, OAuthEnabled: oauthEnabled, TokenForm: true})
}

func writeLoginPage(w http.ResponseWriter, o loginPageOptions) {
	failed, oauthEnabled := o.Failed, o.OAuthEnabled
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if failed {
		// The console submits this form with fetch and reads the reason here.
		reason := "invalid"
		if o.Throttled {
			reason = "throttled"
		}
		w.Header().Set("X-Stash-Login-Error", reason)
		w.WriteHeader(http.StatusUnauthorized)
	}
	message := "발급한 토큰으로 로그인하세요."
	if !o.TokenForm {
		message = "아이디와 비밀번호로 로그인하세요."
	}
	if failed {
		message = "토큰이 올바르지 않거나 만료되었습니다."
		if !o.TokenForm {
			message = "아이디 또는 비밀번호가 올바르지 않습니다."
		}
		if o.Throttled {
			message = "로그인 실패가 너무 많습니다. 잠시 후 다시 시도하세요."
		}
	}
	_, _ = io.WriteString(w, `<!doctype html><html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Stash 로그인</title><style>
:root{color-scheme:light dark;--bg:#eef2f7;--surface:#fff;--ink:#182235;--muted:#667085;--border:#d7dee8;--accent:#5b5bd6;--danger:#c83c56}*{box-sizing:border-box}body{min-height:100vh;margin:0;display:grid;place-items:center;padding:24px;background:var(--bg);color:var(--ink);font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}@media(prefers-color-scheme:dark){:root{--bg:#0d131b;--surface:#151d27;--ink:#f4f7fb;--muted:#a8b5c7;--border:#2e3b4a;--accent:#a5b0ff;--danger:#ff8a9b}}main{width:min(420px,100%);padding:28px;border:1px solid var(--border);border-radius:16px;background:var(--surface);box-shadow:0 16px 40px #0002}h1{margin:0 0 6px;font-size:22px;letter-spacing:-.04em}p{margin:0 0 20px;color:var(--muted)}label{display:grid;gap:7px;font-weight:700}input{width:100%;min-height:42px;padding:10px 12px;border:1px solid var(--border);border-radius:10px;background:transparent;color:var(--ink);font:inherit}input:focus{outline:3px solid color-mix(in srgb,var(--accent) 32%,transparent);border-color:var(--accent)}button{width:100%;min-height:42px;margin-top:14px;border:0;border-radius:10px;background:var(--accent);color:#fff;font:inherit;font-weight:800;cursor:pointer}.error{margin:-4px 0 14px;color:var(--danger);font-size:13px}a{display:block;margin-top:16px;color:var(--muted);text-align:center;text-decoration:none}
</style></head><body><main><h1>Stash 로그인</h1><p>`+message+`</p>`)
	if o.TokenForm {
		_, _ = io.WriteString(w, `<form method="post" action="/auth/login"><label for="stash-token">토큰</label><input id="stash-token" name="token" type="password" autocomplete="off" autocapitalize="off" spellcheck="false" required autofocus placeholder="stash_api_…">`)
		if failed {
			_, _ = io.WriteString(w, `<div class="error" role="alert">토큰을 확인하고 다시 시도하세요.</div>`)
		}
		_, _ = io.WriteString(w, `<button type="submit">토큰으로 로그인</button></form>`)
		if o.LocalEnabled {
			_, _ = io.WriteString(w, `<a href="/auth/login?provider=local">아이디로 로그인</a>`)
		}
	} else {
		_, _ = io.WriteString(w, `<form method="post" action="/auth/login"><label for="stash-username">아이디</label><input id="stash-username" name="username" type="text" autocomplete="username" autocapitalize="off" spellcheck="false" required autofocus><label for="stash-password" style="margin-top:12px">비밀번호</label><input id="stash-password" name="password" type="password" autocomplete="current-password" required>`)
		if failed {
			_, _ = io.WriteString(w, `<div class="error" role="alert">아이디와 비밀번호를 확인하고 다시 시도하세요.</div>`)
		}
		_, _ = io.WriteString(w, `<button type="submit">로그인</button></form><a href="/auth/login?provider=token">토큰으로 로그인</a>`)
	}
	if oauthEnabled {
		_, _ = io.WriteString(w, `<a href="/auth/login?provider=oidc">계정(SSO)으로 로그인</a>`)
	}
	_, _ = io.WriteString(w, `<a href="/">돌아가기</a></main></body></html>`)
}

func (p *Provider) HandleCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if p == nil {
		http.Error(w, "authentication is disabled", http.StatusNotFound)
		return
	}
	if p.verifier == nil || p.oauth2Config.ClientID == "" {
		http.Error(w, "browser login is not configured", http.StatusServiceUnavailable)
		return
	}
	login, expiresAt, err := p.completeOIDCLogin(r, r.FormValue("state"), w)
	if err != nil {
		http.Error(w, err.Error(), err.status)
		return
	}
	// The identity token names a subject at the issuer; the session is for
	// the user that identity belongs to.
	username, resolveErr := p.resolveOIDCUser(r.Context(), p.config.Issuer, login.Subject, login.Profile)
	if errors.Is(resolveErr, ErrUserDisabled) {
		http.Error(w, "login was denied: this account is disabled", http.StatusForbidden)
		return
	}
	if resolveErr != nil {
		log.Printf("resolve SSO user %q: %v", login.Subject, resolveErr)
		http.Error(w, "could not look up the account", http.StatusServiceUnavailable)
		return
	}
	p.setSessionCookie(w, username, expiresAt, true)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type authHTTPError struct {
	status  int
	message string
}

func (e authHTTPError) Error() string { return e.message }

// oidcLogin is a verified upstream login: the issuer's subject plus the
// profile claims used to name a newly provisioned user.
type oidcLogin struct {
	Subject string
	Profile oidcProfile
}

func (p *Provider) completeOIDCLogin(r *http.Request, providedState string, w http.ResponseWriter) (oidcLogin, time.Time, *authHTTPError) {
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusBadRequest, "login state is missing"}
	}
	nonceCookie, err := r.Cookie(nonceCookieName)
	if err != nil || nonceCookie.Value == "" {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusBadRequest, "login nonce is missing"}
	}
	clearOAuthCookies(w, p.config.CookieSecure)
	if len(providedState) != len(stateCookie.Value) || subtle.ConstantTimeCompare([]byte(providedState), []byte(stateCookie.Value)) != 1 {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusBadRequest, "invalid login state"}
	}
	if upstreamError := strings.TrimSpace(r.FormValue("error")); upstreamError != "" {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusBadRequest, "login was denied: " + upstreamError}
	}
	code := r.FormValue("code")
	if code == "" {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusBadRequest, "authorization code is missing"}
	}

	providerCtx, cancel := context.WithTimeout(r.Context(), oauthProviderTimeout)
	defer cancel()
	token, err := p.oauth2Config.Exchange(providerCtx, code)
	if err != nil {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusBadGateway, "could not complete login"}
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusBadGateway, "identity token is missing"}
	}

	idToken, err := p.verifyIdentityToken(providerCtx, rawIDToken)
	if err != nil {
		// Some OAuth providers expose an opaque access token and publish no
		// usable JWKS for their ID token. The authorization-code exchange has
		// already authenticated the confidential client, so an active token
		// introspection result bound to that same client is a safe OAuth
		// compatibility fallback. Keep the verifier error in the log so the
		// provider can still be configured for normal OIDC validation later.
		if subject, _, introspectionErr := p.introspectLoginAccessToken(providerCtx, token.AccessToken); introspectionErr == nil {
			log.Printf("OIDC identity token verification failed; accepted introspected access token: %v", err)
			return oidcLogin{Subject: subject}, time.Now().Add(p.sessionTTL()), nil
		} else {
			log.Printf("OIDC identity token verification failed: %v (access-token introspection fallback failed: %v)", err, introspectionErr)
		}
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusUnauthorized, "identity token is invalid"}
	}
	if idToken.Nonce == "" || len(idToken.Nonce) != len(nonceCookie.Value) || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonceCookie.Value)) != 1 {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusUnauthorized, "invalid login nonce"}
	}

	subject, err := subjectFromToken(idToken)
	if err != nil {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusUnauthorized, "identity token has no stable subject"}
	}
	if idToken.Expiry.IsZero() || !idToken.Expiry.After(time.Now()) {
		return oidcLogin{}, time.Time{}, &authHTTPError{http.StatusUnauthorized, "identity token is expired"}
	}
	var profile oidcProfile
	_ = idToken.Claims(&profile)
	// The ID token only proves the login moment; the Stash session lifetime
	// is governed locally so the console does not expire with it.
	return oidcLogin{Subject: subject, Profile: profile}, time.Now().Add(p.sessionTTL()), nil
}

// verifyIdentityToken accepts the asymmetric algorithms supported by go-oidc
// and, when a confidential client secret is configured, the HS256 form used by
// providers such as Authentik when no signing key is selected. The HMAC path
// is deliberately separate so an asymmetric token can never be verified with
// the client secret.
func (p *Provider) verifyIdentityToken(ctx context.Context, raw string) (*oidc.IDToken, error) {
	var hmacErr error
	if p.hmacVerifier != nil {
		if token, err := p.hmacVerifier.Verify(ctx, raw); err == nil {
			return token, nil
		} else {
			hmacErr = err
		}
	}
	if p.verifier == nil {
		if hmacErr != nil {
			return nil, fmt.Errorf("HMAC identity-token verification failed: %w", hmacErr)
		}
		return nil, errors.New("OIDC identity-token verifier is unavailable")
	}
	token, err := p.verifier.Verify(ctx, raw)
	if err != nil && hmacErr != nil {
		return nil, fmt.Errorf("HMAC identity-token verification failed: %v; standard verification failed: %w", hmacErr, err)
	}
	return token, err
}

// introspectLoginAccessToken is the OAuth compatibility path used only after
// ID-token verification fails. It accepts a token that the upstream provider
// reports as active and whose audience is the configured browser client. A
// resource-server audience is deliberately not enough here: that token must
// be issued for this login client, not merely for Stash's MCP endpoint.
func (p *Provider) introspectLoginAccessToken(ctx context.Context, rawToken string) (string, time.Time, error) {
	if p == nil || p.introspectionEndpoint == "" || strings.TrimSpace(p.config.ClientID) == "" || p.config.ClientSecret == "" {
		return "", time.Time{}, errors.New("OIDC login introspection is not configured")
	}
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return "", time.Time{}, errors.New("access token is missing")
	}
	form := url.Values{"token": {rawToken}, "token_type_hint": {"access_token"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.introspectionEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(p.config.ClientID, p.config.ClientSecret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", time.Time{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("introspection returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Active bool            `json:"active"`
		Sub    string          `json:"sub"`
		Exp    int64           `json:"exp"`
		Aud    json.RawMessage `json:"aud"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&result); err != nil {
		return "", time.Time{}, err
	}
	if !result.Active || strings.TrimSpace(result.Sub) == "" || result.Exp <= time.Now().Unix() {
		return "", time.Time{}, errors.New("inactive OIDC login access token")
	}
	audience, err := decodeAudience(result.Aud)
	if err != nil {
		return "", time.Time{}, err
	}
	if !audienceContains(audience, strings.TrimSpace(p.config.ClientID)) {
		return "", time.Time{}, errors.New("OIDC login access token has unexpected audience")
	}
	return result.Sub, time.Unix(result.Exp, 0), nil
}

func (p *Provider) sessionTTL() time.Duration {
	if p == nil || p.config.SessionTTL <= 0 {
		return defaultSessionTTL
	}
	return p.config.SessionTTL
}

// RenewSession slides a renewable browser session forward once less than half
// of its lifetime remains, so an actively used console stays signed in.
// Bearer credentials and non-renewable sessions are left untouched.
func (p *Provider) RenewSession(w http.ResponseWriter, r *http.Request) {
	if p == nil || p.config.APISecret == "" || bearerToken(r) != "" {
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return
	}
	subject, expiresAt, renewable, err := parseSessionClaims(strings.TrimSpace(cookie.Value), p.config.APISecret)
	if err != nil || !renewable {
		return
	}
	ttl := p.sessionTTL()
	if time.Until(expiresAt) > ttl/2 {
		return
	}
	p.setSessionCookie(w, subject, time.Now().Add(ttl), true)
}

func (p *Provider) setSessionCookie(w http.ResponseWriter, subject string, expiresAt time.Time, renewable bool) {
	if p.config.APISecret == "" {
		return
	}
	session, err := signSessionToken(subject, p.config.APISecret, expiresAt, renewable)
	if err != nil {
		return
	}
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session,
		Expires:  expiresAt,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   p.config.CookieSecure,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
	})
}

func validRedirectURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Fragment != "" || parsed.User != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return isLoopbackRedirect(raw)
}

func validResourceURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.User != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return isLoopbackURL(parsed)
}

func isLoopbackRedirect(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() == "" {
		return false
	}
	return isLoopbackURL(parsed)
}

func isLoopbackURL(parsed *url.URL) bool {
	if parsed == nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Hostname() == "" {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func validateSigningSecret(secret string) error {
	if len(strings.TrimSpace(secret)) < minimumSecretBytes {
		return fmt.Errorf("must contain at least %d bytes", minimumSecretBytes)
	}
	return nil
}

func (p *Provider) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := browserRequestProtection.Check(r); err != nil {
		http.Error(w, "요청 출처를 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	secure := false
	if p != nil {
		secure = p.config.CookieSecure
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		Path:     "/",
		SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (p *Provider) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	status := map[string]any{"auth_mode": "none", "authenticated": false}
	if p != nil {
		status["auth_mode"] = p.Mode()
		status["local_login"] = p.LocalLoginAvailable(r.Context())
		status["sso_login"] = p.browserLoginConfigured()
		if user, err := p.VerifyRequest(r); err == nil && user != "" {
			status["authenticated"] = true
			status["user"] = user
			status["admin"] = p.IsAdmin(r.Context(), user)
			status["has_password"] = p.HasPassword(r.Context(), user)
			p.RenewSession(w, r)
		}
	}
	_ = json.NewEncoder(w).Encode(status)
}

// HandleTokens lists the authenticated user's API-token metadata. Raw token
// values are never recoverable after issuance.
func (p *Provider) HandleTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if p == nil || p.tokenPool == nil {
		http.Error(w, `{"error":"token management is unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	user, err := p.VerifyRequest(r)
	if err != nil || user == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	tokens, err := p.listAPITokens(r.Context(), user)
	if err != nil {
		http.Error(w, `{"error":"could not list API tokens"}`, http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"tokens": tokens})
}

// HandleRevokeToken revokes one API token belonging to the authenticated user.
// Repeating the request is safe, which also makes double-clicks harmless.
func (p *Provider) HandleRevokeToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if err := browserRequestProtection.Check(r); err != nil {
		http.Error(w, `{"error":"cross-origin request denied"}`, http.StatusForbidden)
		return
	}
	if p == nil || p.tokenPool == nil {
		http.Error(w, `{"error":"token management is unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/auth/tokens/")
	path = strings.TrimSuffix(path, "/revoke")
	id, err := strconv.ParseInt(path, 10, 64)
	if err != nil || id <= 0 || strings.Contains(path, "/") {
		http.Error(w, `{"error":"invalid API token ID"}`, http.StatusBadRequest)
		return
	}
	user, err := p.VerifyRequest(r)
	if err != nil || user == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	revokedAt, err := p.revokeAPIToken(r.Context(), user, id)
	if err != nil {
		status := http.StatusNotFound
		if strings.Contains(err.Error(), "storage is unavailable") {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, `{"error":"could not revoke API token"}`, status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "revoked": true, "revoked_at": revokedAt, "expires_at": revokedAt})
}

// MCPUnauthorized answers an MCP request that carried no valid API token.
// There is no OAuth discovery to advertise: MCP clients are configured with
// a token issued from the console or `stash mcp token`.
func (p *Provider) MCPUnauthorized(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="stash", error="invalid_token", error_description="a Stash API token is required"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = io.WriteString(w, `{"error":"unauthorized","error_description":"a Stash API token is required"}`+"\n")
}

// VerifyRequest validates a signed Stash session/API token or an OIDC access
// token sent as a bearer credential. It is used by the browser and maintenance
// routes, where the OIDC session remains the login boundary.
func (p *Provider) VerifyRequest(r *http.Request) (string, error) {
	return p.verifyRequest(r, false)
}

// VerifyMCPRequest validates the credential used by MCP transports: a Stash
// API token stored in the database, sent as a bearer token. A browser session
// cookie is retained only for the embedded console, which already runs on
// the same origin.
func (p *Provider) VerifyMCPRequest(r *http.Request) (string, error) {
	return p.verifyRequest(r, true)
}

func (p *Provider) verifyRequest(r *http.Request, mcp bool) (string, error) {
	if p == nil {
		return "", errors.New("authentication is disabled")
	}
	if p.Mode() == "stdio" {
		return "", errors.New("stdio credentials cannot authenticate an HTTP request")
	}

	rawToken := bearerToken(r)
	bearer := rawToken != ""
	if !bearer {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			rawToken = strings.TrimSpace(cookie.Value)
		}
	}
	if rawToken == "" {
		return "", errors.New("missing authentication token")
	}

	if strings.HasPrefix(rawToken, sessionTokenPrefix) {
		if bearer {
			if mcp {
				return "", errors.New("a Stash API token is required")
			}
			return "", errors.New("session tokens are only accepted as cookies")
		}
		subject, err := parseSessionToken(rawToken, p.config.APISecret)
		return p.activeSubject(r.Context(), subject, err)
	}
	if strings.HasPrefix(rawToken, apiTokenPrefix) && bearer {
		subject, _, err := p.verifyAPIToken(r.Context(), rawToken)
		return p.activeSubject(r.Context(), subject, err)
	}
	if mcp {
		return "", errors.New("a Stash API token is required")
	}
	return "", errors.New("unsupported credential")
}

// activeSubject turns a verified subject into an error when an administrator
// has disabled that user, so a disable takes effect on the next request
// instead of at the end of the session or token lifetime.
func (p *Provider) activeSubject(ctx context.Context, subject string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if p.userDisabled(ctx, subject) {
		return "", ErrUserDisabled
	}
	return subject, nil
}

// VerifyBearerToken validates the credential supplied to the STDIO adapter
// through STASH_AUTH_STDIO_TOKEN: a Stash API token stored in the database.
func (p *Provider) VerifyBearerToken(ctx context.Context, rawToken string) (string, error) {
	if p == nil {
		return "", errors.New("authentication is disabled")
	}
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return "", errors.New("missing authentication token")
	}
	if !strings.HasPrefix(rawToken, apiTokenPrefix) {
		return "", errors.New("a Stash API token is required")
	}
	subject, _, err := p.verifyAPIToken(ctx, rawToken)
	return p.activeSubject(ctx, subject, err)
}

func providerIntrospectionEndpoint(provider *oidc.Provider) string {
	if provider == nil {
		return ""
	}
	var metadata struct {
		IntrospectionEndpoint string `json:"introspection_endpoint"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return ""
	}
	endpoint := strings.TrimSpace(metadata.IntrospectionEndpoint)
	if endpoint == "" || !validRedirectURI(endpoint) {
		return ""
	}
	return endpoint
}

func decodeAudience(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func audienceContains(audience []string, wanted string) bool {
	wanted = strings.TrimSpace(wanted)
	if wanted == "" {
		return false
	}
	for _, actual := range audience {
		if actual == wanted || strings.TrimRight(actual, "/") == wanted {
			return true
		}
	}
	return false
}

func subjectFromToken(token *oidc.IDToken) (string, error) {
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := token.Claims(&claims); err != nil {
		return "", fmt.Errorf("read identity claims: %w", err)
	}
	if claims.Sub == "" {
		return "", errors.New("identity token has no subject")
	}
	return claims.Sub, nil
}

func (p *Provider) verifyAPIToken(ctx context.Context, rawToken string) (string, time.Time, error) {
	rawToken = strings.TrimSpace(rawToken)
	if p == nil {
		return "", time.Time{}, errors.New("authentication is disabled")
	}
	if p.tokenPool == nil {
		return "", time.Time{}, errors.New("API token storage is unavailable")
	}
	if !strings.HasPrefix(rawToken, apiTokenPrefix) {
		return "", time.Time{}, errors.New("not a Stash API token")
	}
	hash := sha256.Sum256([]byte(rawToken))
	var subject string
	var revokedAt *time.Time
	var expiresAt *time.Time
	err := p.tokenPool.QueryRow(ctx, `
		SELECT subject, revoked_at, expires_at
		FROM auth_tokens
		WHERE token_hash = $1
	`, hash[:]).Scan(&subject, &revokedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, errors.New("unknown API token")
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("look up API token: %w", err)
	}
	if revokedAt != nil {
		return "", time.Time{}, errors.New("API token revoked")
	}
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		return "", time.Time{}, errors.New("API token expired")
	}
	// ponytail: update usage inline; split this into an async write only if auth
	// traffic makes the extra round trip measurable.
	_, _ = p.tokenPool.Exec(ctx, `
		UPDATE auth_tokens SET last_used_at = clock_timestamp()
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, hash[:])
	if expiresAt != nil {
		return subject, *expiresAt, nil
	}
	return subject, time.Time{}, nil
}

// IssueAPIToken stores a new API token for subject and returns the raw value
// once. A zero ttl means the token lives until it is revoked.
func (p *Provider) IssueAPIToken(ctx context.Context, subject, name string, ttl time.Duration) (string, APIToken, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "", APIToken{}, errors.New("token subject is required")
	}
	return p.issueAPIToken(ctx, subject, name, ttl)
}

func (p *Provider) issueAPIToken(ctx context.Context, subject, name string, ttl time.Duration) (string, APIToken, error) {
	if p == nil {
		return "", APIToken{}, errors.New("authentication is disabled")
	}
	name = strings.TrimSpace(name)
	if len(name) > 120 {
		return "", APIToken{}, errors.New("token name is too long")
	}
	if p.tokenPool == nil {
		return "", APIToken{}, errors.New("API token storage is unavailable")
	}
	secret, err := randomToken(32)
	if err != nil {
		return "", APIToken{}, err
	}
	token := apiTokenPrefix + secret
	hash := sha256.Sum256([]byte(token))
	var metadata APIToken
	if ttl > 0 {
		expiresAt := time.Now().UTC().Add(ttl)
		metadata.ExpiresAt = &expiresAt
	}
	err = p.tokenPool.QueryRow(ctx, `
		INSERT INTO auth_tokens (subject, name, token_hash, expires_at)
		VALUES ($1, $2, $3, $4)
		RETURNING id, name, created_at, expires_at, last_used_at, revoked_at
	`, subject, name, hash[:], metadata.ExpiresAt).Scan(&metadata.ID, &metadata.Name, &metadata.CreatedAt, &metadata.ExpiresAt, &metadata.LastUsedAt, &metadata.RevokedAt)
	if err != nil {
		return "", APIToken{}, fmt.Errorf("store API token: %w", err)
	}
	return token, metadata, nil
}

func (p *Provider) listAPITokens(ctx context.Context, subject string) ([]APIToken, error) {
	if p == nil || p.tokenPool == nil {
		return []APIToken{}, nil
	}
	rows, err := p.tokenPool.Query(ctx, `
		SELECT id, name, created_at, expires_at, last_used_at, revoked_at
		FROM auth_tokens
		WHERE subject = $1
		ORDER BY created_at DESC, id DESC
	`, subject)
	if err != nil {
		return nil, fmt.Errorf("list API tokens: %w", err)
	}
	defer rows.Close()
	tokens := make([]APIToken, 0)
	for rows.Next() {
		var token APIToken
		if err := rows.Scan(&token.ID, &token.Name, &token.CreatedAt, &token.ExpiresAt, &token.LastUsedAt, &token.RevokedAt); err != nil {
			return nil, fmt.Errorf("read API token: %w", err)
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read API tokens: %w", err)
	}
	return tokens, nil
}

func (p *Provider) revokeAPIToken(ctx context.Context, subject string, id int64) (time.Time, error) {
	if p == nil || p.tokenPool == nil {
		return time.Time{}, errors.New("durable API-token storage is unavailable")
	}
	if id <= 0 {
		return time.Time{}, errors.New("invalid API token ID")
	}
	var revokedAt time.Time
	err := p.tokenPool.QueryRow(ctx, `
		UPDATE auth_tokens
		SET revoked_at = COALESCE(revoked_at, statement_timestamp()),
		    expires_at = COALESCE(revoked_at, statement_timestamp())
		WHERE id = $1 AND subject = $2
		RETURNING revoked_at
	`, id, subject).Scan(&revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, errors.New("API token not found")
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("revoke API token: %w", err)
	}
	return revokedAt, nil
}

func (p *Provider) HandleGenerateToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if err := browserRequestProtection.Check(r); err != nil {
		http.Error(w, `{"error":"cross-origin request denied"}`, http.StatusForbidden)
		return
	}
	if p == nil {
		http.Error(w, `{"error":"authentication is disabled"}`, http.StatusNotFound)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, `{"error":"invalid token request"}`, http.StatusBadRequest)
		return
	}
	user, err := p.VerifyRequest(r)
	if err != nil || user == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var ttl time.Duration
	if values, ok := r.PostForm["expires_in"]; ok {
		value := strings.TrimSpace(r.PostForm.Get("expires_in"))
		if value == "" {
			value = "0"
		}
		seconds, err := strconv.ParseInt(value, 10, 64)
		if len(values) != 1 || err != nil || seconds < 0 || seconds > int64((1<<63-1)/time.Second) {
			http.Error(w, `{"error":"invalid token lifetime"}`, http.StatusBadRequest)
			return
		}
		ttl = time.Duration(seconds) * time.Second
	}
	token, metadata, err := p.issueAPIToken(r.Context(), user, r.FormValue("name"), ttl)
	if err != nil {
		status := http.StatusServiceUnavailable
		if strings.Contains(err.Error(), "token name is too long") {
			status = http.StatusBadRequest
		}
		http.Error(w, `{"error":"token generation is unavailable"}`, status)
		return
	}
	response := map[string]any{
		"token":      token,
		"token_type": "Bearer",
		"expires_in": int64(ttl / time.Second),
		"expires_at": metadata.ExpiresAt,
	}
	if metadata.ID != 0 {
		response["id"] = metadata.ID
		response["name"] = metadata.Name
		response["created_at"] = metadata.CreatedAt
	}
	_ = json.NewEncoder(w).Encode(response)
}

func bearerToken(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

func setOAuthCookies(w http.ResponseWriter, secure bool) (string, string, error) {
	state, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	expires := time.Now().Add(loginStateTTL)
	for name, value := range map[string]string{stateCookieName: state, nonceCookieName: nonce} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    value,
			Expires:  expires,
			MaxAge:   int(loginStateTTL.Seconds()),
			HttpOnly: true,
			Secure:   secure,
			Path:     "/",
			SameSite: http.SameSiteLaxMode,
		})
	}
	return state, nonce, nil
}

func clearOAuthCookies(w http.ResponseWriter, secure bool) {
	for _, name := range []string{stateCookieName, nonceCookieName} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   secure,
			Path:     "/",
			SameSite: http.SameSiteLaxMode,
		})
	}
}

func randomToken(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// signSessionToken encodes user.expiry[.r]; the optional "r" marks a session
// that RenewSession may slide forward. Legacy three-part tokens stay valid
// and are treated as non-renewable.
func signSessionToken(user, secret string, expiresAt time.Time, renewable bool) (string, error) {
	if user == "" {
		return "", errors.New("user is required")
	}
	if secret == "" {
		return "", errors.New("API secret is required")
	}
	if !expiresAt.After(time.Now()) {
		return "", errors.New("session expiry must be in the future")
	}
	fields := []string{
		base64.RawURLEncoding.EncodeToString([]byte(user)),
		strconv.FormatInt(expiresAt.Unix(), 10),
	}
	if renewable {
		fields = append(fields, "r")
	}
	payload := strings.Join(fields, ".")
	return sessionTokenPrefix + payload + "." + sign(payload, secret), nil
}

func parseSessionToken(token, secret string) (string, error) {
	user, _, _, err := parseSessionClaims(token, secret)
	return user, err
}

func parseSessionClaims(token, secret string) (string, time.Time, bool, error) {
	if secret == "" {
		return "", time.Time{}, false, errors.New("API secret is not configured")
	}
	if !strings.HasPrefix(token, sessionTokenPrefix) {
		return "", time.Time{}, false, errors.New("invalid session token prefix")
	}
	parts := strings.Split(strings.TrimPrefix(token, sessionTokenPrefix), ".")
	renewable := len(parts) == 4 && parts[2] == "r"
	if len(parts) != 3 && !renewable {
		return "", time.Time{}, false, errors.New("invalid session token format")
	}
	signature := parts[len(parts)-1]
	payload := strings.Join(parts[:len(parts)-1], ".")
	expected, _ := hex.DecodeString(sign(payload, secret))
	provided, err := hex.DecodeString(signature)
	if err != nil || subtle.ConstantTimeCompare(provided, expected) != 1 {
		return "", time.Time{}, false, errors.New("invalid session token signature")
	}
	decodedUser, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(decodedUser) == 0 {
		return "", time.Time{}, false, errors.New("invalid session token user")
	}
	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expiresAt <= time.Now().Unix() {
		return "", time.Time{}, false, errors.New("session token expired")
	}
	return string(decodedUser), time.Unix(expiresAt, 0), renewable, nil
}

func sign(payload, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}
