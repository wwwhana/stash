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
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

const (
	stateCookieName        = "oauthstate"
	nonceCookieName        = "oidc_nonce"
	sessionCookieName      = "stash_session"
	apiTokenPrefix         = "stash_api_"
	oauthTokenPrefix       = "stash_oauth_"
	refreshTokenPrefix     = "stash_refresh_"
	sessionTokenPrefix     = "stash_session_"
	defaultTokenTTL        = 30 * 24 * time.Hour
	defaultAccessTokenTTL  = time.Hour
	defaultRefreshTokenTTL = 30 * 24 * time.Hour
	authorizationCodeTTL   = 90 * time.Second
	loginStateTTL          = 10 * time.Minute
	dynamicClientIdleTTL   = 24 * time.Hour
	oauthRateWindow        = time.Minute
	oauthProviderTimeout   = 15 * time.Second
	maxPendingRequests     = 128
	maxOAuthClients        = 256
	maxRefreshTokens       = 1024
	maxRefreshPerClient    = 8
	maxAuthorizeRequests   = 120
	maxRegisterRequests    = 20
	maxTokenRequests       = 240
	minimumSecretBytes     = 32
)

// Config contains the settings needed by the HTTP authentication boundary.
type Config struct {
	Mode            string
	Issuer          string
	ClientID        string
	MCPClientID     string
	ClientSecret    string
	RedirectURL     string
	APISecret       string
	MCPResourceURL  string
	CookieSecure    bool
	APITokenTTL     time.Duration
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	StdioToken      string
}

type Provider struct {
	config                Config
	oidcProvider          *oidc.Provider
	oauth2Config          oauth2.Config
	verifier              *oidc.IDTokenVerifier
	hmacVerifier          *oidc.IDTokenVerifier
	accessVerifier        *oidc.IDTokenVerifier
	hmacAccessVerifier    *oidc.IDTokenVerifier
	introspectionEndpoint string
	mu                    sync.Mutex
	clients               map[string]oauthClient
	pending               map[string]authorizationRequest
	codes                 map[string]authorizationCode
	refreshTokens         map[string]refreshToken
	authorizeRate         fixedWindowRateLimit
	registerRate          fixedWindowRateLimit
	tokenRate             fixedWindowRateLimit
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

type oauthClient struct {
	ID                      string
	RedirectURIs            []string
	TokenEndpointAuthMethod string
	Secret                  string
	Name                    string
	Dynamic                 bool
	LastUsed                time.Time
}

type authorizationRequest struct {
	ClientID         string
	RedirectURI      string
	State            string
	CodeChallenge    string
	ChallengeMethod  string
	Resource         string
	Scope            string
	ExpiresAt        time.Time
	Subject          string
	SessionExpiresAt time.Time
	ConsentToken     string
}

type authorizationCode struct {
	Subject         string
	ClientID        string
	RedirectURI     string
	CodeChallenge   string
	ChallengeMethod string
	Resource        string
	Scope           string
	ExpiresAt       time.Time
}

type refreshToken struct {
	Subject   string
	ClientID  string
	Resource  string
	Scope     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type fixedWindowRateLimit struct {
	StartedAt time.Time
	Count     int
}

func (l *fixedWindowRateLimit) allow(now time.Time, limit int) bool {
	if l.StartedAt.IsZero() || now.Sub(l.StartedAt) >= oauthRateWindow {
		l.StartedAt = now
		l.Count = 0
	}
	if l.Count >= limit {
		return false
	}
	l.Count++
	return true
}

var browserRequestProtection = http.NewCrossOriginProtection()

// Init returns nil when authentication is explicitly disabled.
func Init(ctx context.Context, cfg Config) (*Provider, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	cfg.Issuer = strings.TrimSpace(cfg.Issuer)
	cfg.ClientID = strings.TrimSpace(cfg.ClientID)
	cfg.MCPClientID = strings.TrimSpace(cfg.MCPClientID)
	cfg.RedirectURL = strings.TrimSpace(cfg.RedirectURL)
	cfg.MCPResourceURL = strings.TrimSpace(cfg.MCPResourceURL)
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
		return &Provider{
			config:        cfg,
			clients:       make(map[string]oauthClient),
			pending:       make(map[string]authorizationRequest),
			codes:         make(map[string]authorizationCode),
			refreshTokens: make(map[string]refreshToken),
		}, nil
	}
	if mode == "token" {
		if err := validateSigningSecret(cfg.APISecret); err != nil {
			return nil, fmt.Errorf("token mode requires STASH_AUTH_API_SECRET: %w", err)
		}
		if cfg.APITokenTTL <= 0 {
			cfg.APITokenTTL = defaultTokenTTL
		}
		log.Println("Auth mode: token (Stash API tokens; OIDC is not used)")
		return &Provider{
			config:        cfg,
			clients:       make(map[string]oauthClient),
			pending:       make(map[string]authorizationRequest),
			codes:         make(map[string]authorizationCode),
			refreshTokens: make(map[string]refreshToken),
		}, nil
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
	if strings.TrimSpace(cfg.MCPResourceURL) == "" {
		return nil, errors.New("OAuth mode requires STASH_AUTH_MCP_RESOURCE_URL")
	}
	if !validResourceURL(cfg.MCPResourceURL) {
		return nil, errors.New("STASH_AUTH_MCP_RESOURCE_URL must use HTTPS or loopback HTTP")
	}
	if err := validateSigningSecret(cfg.APISecret); err != nil {
		return nil, fmt.Errorf("HTTP MCP authentication requires STASH_AUTH_API_SECRET: %w", err)
	}
	// The browser login is optional: an MCP resource server can operate without
	// keeping a second confidential OAuth client. A client ID/secret pair may be
	// supplied without a redirect URI for opaque-token introspection only. Once
	// a redirect URI is supplied, require the complete browser-login set so a
	// half-configured /auth/login path cannot fail later with a cryptic error.
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
	if cfg.AccessTokenTTL <= 0 {
		cfg.AccessTokenTTL = defaultAccessTokenTTL
	}
	if cfg.RefreshTokenTTL <= 0 {
		cfg.RefreshTokenTTL = defaultRefreshTokenTTL
	}
	if cfg.AccessTokenTTL > time.Hour {
		return nil, errors.New("OAuth access-token lifetime must not exceed 1h")
	}
	if cfg.RefreshTokenTTL < cfg.AccessTokenTTL {
		return nil, errors.New("OAuth refresh-token lifetime must not be shorter than the access-token lifetime")
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
	// Codex sends an OAuth access token to the MCP resource. Its audience may
	// be a separate public OAuth client, so verify the issuer, signature, and
	// expiry here and apply the configured audience check below. The audience is
	// mandatory; accepting any valid token from the issuer would let a token
	// issued for another application call this MCP server.
	accessVerifier := provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	// Authentik can issue HS256 ID/access tokens when its provider has no
	// asymmetric signing key. go-oidc intentionally excludes symmetric
	// algorithms from discovery, so verify HS256 separately with the
	// confidential client's secret. This remains bound to the configured issuer
	// and audience through go-oidc's normal claim checks.
	hmacVerifier := newHMACVerifier(cfg.Issuer, cfg.ClientID, cfg.ClientSecret, false)
	hmacAccessVerifier := newHMACVerifier(cfg.Issuer, "", cfg.ClientSecret, true)
	introspectionEndpoint := providerIntrospectionEndpoint(provider)
	clients := make(map[string]oauthClient)
	// A configured MCP client is a public client by default. Its redirect URI
	// is checked in clientRedirectAllowed; dynamic registrations are stored
	// in the same table.
	if clientID := strings.TrimSpace(cfg.MCPClientID); clientID != "" {
		clients[clientID] = oauthClient{ID: clientID, TokenEndpointAuthMethod: "none"}
	}
	log.Printf("Auth mode: oauth (issuer: %s)", cfg.Issuer)
	return &Provider{
		config:                cfg,
		oidcProvider:          provider,
		oauth2Config:          oauth2Config,
		verifier:              verifier,
		hmacVerifier:          hmacVerifier,
		accessVerifier:        accessVerifier,
		hmacAccessVerifier:    hmacAccessVerifier,
		introspectionEndpoint: introspectionEndpoint,
		clients:               clients,
		pending:               make(map[string]authorizationRequest),
		codes:                 make(map[string]authorizationCode),
		refreshTokens:         make(map[string]refreshToken),
	}, nil
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

func (p *Provider) localAuthorizationServer() bool {
	return p.oauthMode() && p.oauth2Config.ClientID != "" && p.verifier != nil && p.config.APISecret != ""
}

// HandleAuthorize is the MCP OAuth authorization endpoint. Stash acts as a
// small OAuth resource-owner broker here: Authentik performs the actual user
// login, while Stash keeps the MCP client's redirect and PKCE transaction.
func (p *Provider) HandleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !p.localAuthorizationServer() {
		http.Error(w, "OAuth authorization endpoint is not configured", http.StatusNotImplemented)
		return
	}
	if !p.allowOAuthRequest(&p.authorizeRate, maxAuthorizeRequests) {
		writeOAuthRateLimit(w)
		return
	}

	query := r.URL.Query()
	clientID := strings.TrimSpace(query.Get("client_id"))
	redirectURI := strings.TrimSpace(query.Get("redirect_uri"))
	state := query.Get("state")
	if clientID == "" || len(clientID) > 256 || len(redirectURI) > 2048 || len(state) > 2048 {
		p.oauthError(w, r, redirectURI, state, "invalid_request", "authorization request is invalid", false)
		return
	}
	if !validRedirectURI(redirectURI) || !p.clientRedirectAllowed(clientID, redirectURI) {
		http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
		return
	}
	if query.Get("response_type") != "code" {
		p.oauthError(w, r, redirectURI, state, "unsupported_response_type", "response_type must be code", true)
		return
	}
	challenge := strings.TrimSpace(query.Get("code_challenge"))
	challengeMethod := strings.ToUpper(strings.TrimSpace(query.Get("code_challenge_method")))
	if !validPKCEChallenge(challenge) || challengeMethod != "S256" {
		p.oauthError(w, r, redirectURI, state, "invalid_request", "PKCE S256 is required", true)
		return
	}

	rawResource := strings.TrimSpace(query.Get("resource"))
	if rawResource == "" || len(rawResource) > 2048 || len(query.Get("scope")) > 256 {
		p.oauthError(w, r, redirectURI, state, "invalid_request", "resource is required", true)
		return
	}
	resource := normalizeResourceURL(rawResource)
	if !p.resourceAllowed(resource, r) {
		p.oauthError(w, r, redirectURI, state, "invalid_target", "resource is not this MCP server", true)
		return
	}
	scope := normalizeScope(query.Get("scope"))
	internalState, nonce, err := setOAuthCookies(w, p.config.CookieSecure)
	if err != nil {
		http.Error(w, "could not start authorization", http.StatusInternalServerError)
		return
	}

	p.mu.Lock()
	p.ensureOAuthMapsLocked()
	p.pruneOAuthStateLocked(time.Now())
	if len(p.pending) >= maxPendingRequests {
		p.mu.Unlock()
		clearOAuthCookies(w, p.config.CookieSecure)
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "too many authorization requests")
		return
	}
	p.pending[internalState] = authorizationRequest{
		ClientID:        clientID,
		RedirectURI:     redirectURI,
		State:           state,
		CodeChallenge:   challenge,
		ChallengeMethod: challengeMethod,
		Resource:        resource,
		Scope:           scope,
		ExpiresAt:       time.Now().Add(loginStateTTL),
	}
	p.mu.Unlock()

	authURL := p.oauth2Config.AuthCodeURL(internalState,
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("resource", resource),
		oauth2.SetAuthURLParam("scope", scope),
	)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authURL, http.StatusFound)
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
		p.handleTokenLogin(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if p.Mode() == "token" {
		writeTokenLoginPage(w, false, false)
		return
	}
	provider := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if provider != "" && provider != "oidc" && provider != "token" {
		http.Error(w, "unsupported login provider", http.StatusBadRequest)
		return
	}
	if provider == "token" || !p.browserLoginConfigured() {
		writeTokenLoginPage(w, false, p.browserLoginConfigured())
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
	subject, expiresAt, err := parseStashTokenClaims(rawToken, p.config.APISecret)
	if err != nil {
		writeTokenLoginPage(w, true, p.browserLoginConfigured())
		return
	}
	p.setSessionCookie(w, subject, expiresAt)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func writeTokenLoginPage(w http.ResponseWriter, failed, oauthEnabled bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if failed {
		w.WriteHeader(http.StatusUnauthorized)
	}
	message := "발급한 토큰으로 로그인하세요."
	if failed {
		message = "토큰이 올바르지 않거나 만료되었습니다."
	}
	_, _ = io.WriteString(w, `<!doctype html><html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Stash 로그인</title><style>
:root{color-scheme:light dark;--bg:#eef2f7;--surface:#fff;--ink:#182235;--muted:#667085;--border:#d7dee8;--accent:#5b5bd6;--danger:#c83c56}*{box-sizing:border-box}body{min-height:100vh;margin:0;display:grid;place-items:center;padding:24px;background:var(--bg);color:var(--ink);font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}@media(prefers-color-scheme:dark){:root{--bg:#0d131b;--surface:#151d27;--ink:#f4f7fb;--muted:#a8b5c7;--border:#2e3b4a;--accent:#a5b0ff;--danger:#ff8a9b}}main{width:min(420px,100%);padding:28px;border:1px solid var(--border);border-radius:16px;background:var(--surface);box-shadow:0 16px 40px #0002}h1{margin:0 0 6px;font-size:22px;letter-spacing:-.04em}p{margin:0 0 20px;color:var(--muted)}label{display:grid;gap:7px;font-weight:700}input{width:100%;min-height:42px;padding:10px 12px;border:1px solid var(--border);border-radius:10px;background:transparent;color:var(--ink);font:inherit}input:focus{outline:3px solid color-mix(in srgb,var(--accent) 32%,transparent);border-color:var(--accent)}button{width:100%;min-height:42px;margin-top:14px;border:0;border-radius:10px;background:var(--accent);color:#fff;font:inherit;font-weight:800;cursor:pointer}.error{margin:-4px 0 14px;color:var(--danger);font-size:13px}a{display:block;margin-top:16px;color:var(--muted);text-align:center;text-decoration:none}
</style></head><body><main><h1>Stash 로그인</h1><p>`+message+`</p>`)
	_, _ = io.WriteString(w, `<form method="post" action="/auth/login"><label for="stash-token">토큰</label><input id="stash-token" name="token" type="password" autocomplete="off" autocapitalize="off" spellcheck="false" required autofocus placeholder="stash_api_…">`)
	if failed {
		_, _ = io.WriteString(w, `<div class="error" role="alert">토큰을 확인하고 다시 시도하세요.</div>`)
	}
	_, _ = io.WriteString(w, `<button type="submit">토큰으로 로그인</button></form>`)
	if oauthEnabled {
		_, _ = io.WriteString(w, `<a href="/auth/login?provider=oidc">계정으로 로그인</a>`)
	}
	_, _ = io.WriteString(w, `<a href="/">돌아가기</a></main></body></html>`)
}

var oauthConsentPage = template.Must(template.New("oauth-consent").Parse(`<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Stash 연결 허용</title><style>
:root{color-scheme:light dark;--bg:#eef2f7;--surface:#fff;--ink:#182235;--muted:#667085;--border:#d7dee8;--accent:#5b5bd6;--danger:#b42318}*{box-sizing:border-box}body{min-height:100vh;margin:0;display:grid;place-items:center;padding:24px;background:var(--bg);color:var(--ink);font:14px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif}@media(prefers-color-scheme:dark){:root{--bg:#0d131b;--surface:#151d27;--ink:#f4f7fb;--muted:#a8b5c7;--border:#2e3b4a;--accent:#a5b0ff;--danger:#ff9b8f}}main{width:min(460px,100%);padding:28px;border:1px solid var(--border);border-radius:16px;background:var(--surface);box-shadow:0 16px 40px #0002}h1{margin:0 0 10px;font-size:22px;letter-spacing:-.04em}p{margin:0 0 18px;color:var(--muted)}strong{color:var(--ink)}dl{margin:0 0 20px;padding:14px;border:1px solid var(--border);border-radius:10px}dt{font-weight:700}dd{margin:4px 0 0;overflow-wrap:anywhere;color:var(--muted)}.actions{display:grid;grid-template-columns:1fr 1fr;gap:10px}button{min-height:42px;border:0;border-radius:10px;font:inherit;font-weight:800;cursor:pointer}.allow{background:var(--accent);color:#fff}.deny{border:1px solid var(--border);background:transparent;color:var(--danger)}
</style></head><body><main><h1>연결 허용</h1><p><strong>{{.ClientName}}</strong>에서 Stash에 연결하려고 합니다.</p><dl><dt>코드를 받을 주소</dt><dd>{{.RedirectOrigin}}</dd></dl><p>허용하면 내 Stash 데이터를 읽고 변경하고 삭제할 수 있습니다.</p><form method="post" action="/oauth/consent"><input type="hidden" name="state" value="{{.State}}"><input type="hidden" name="consent_token" value="{{.ConsentToken}}"><div class="actions"><button class="deny" name="decision" value="deny" type="submit">거절</button><button class="allow" name="decision" value="allow" type="submit">허용</button></div></form></main></body></html>`))

func (p *Provider) writeOAuthConsentPage(w http.ResponseWriter, state, consentToken string, pending authorizationRequest) {
	client, _ := p.client(pending.ClientID)
	clientName := strings.TrimSpace(client.Name)
	if clientName == "" {
		clientName = pending.ClientID
	}
	redirectOrigin := pending.RedirectURI
	if parsed, err := url.Parse(pending.RedirectURI); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		redirectOrigin = parsed.Scheme + "://" + parsed.Host
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := oauthConsentPage.Execute(w, map[string]string{
		"ClientName":     clientName,
		"RedirectOrigin": redirectOrigin,
		"State":          state,
		"ConsentToken":   consentToken,
	}); err != nil {
		log.Printf("render OAuth consent page: %v", err)
	}
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

	providedState := r.FormValue("state")
	p.mu.Lock()
	p.ensureOAuthMapsLocked()
	pending, isOAuthRequest := p.pending[providedState]
	p.mu.Unlock()
	subject, expiresAt, err := p.completeOIDCLogin(r, providedState, w)
	if err != nil {
		if isOAuthRequest && loginStateMatches(r, providedState) {
			p.mu.Lock()
			delete(p.pending, providedState)
			p.mu.Unlock()
			oauthCode := "server_error"
			if strings.HasPrefix(err.message, "login was denied:") {
				oauthCode = "access_denied"
			}
			p.oauthError(w, r, pending.RedirectURI, pending.State, oauthCode, err.message, true)
			return
		}
		http.Error(w, err.Error(), err.status)
		return
	}

	if isOAuthRequest {
		consentToken, err := randomToken(32)
		if err != nil {
			http.Error(w, "could not prepare authorization", http.StatusInternalServerError)
			return
		}
		p.mu.Lock()
		p.ensureOAuthMapsLocked()
		p.pruneOAuthStateLocked(time.Now())
		pending, isOAuthRequest = p.pending[providedState]
		if isOAuthRequest {
			pending.Subject = subject
			pending.SessionExpiresAt = expiresAt
			pending.ConsentToken = consentToken
			pending.ExpiresAt = time.Now().Add(loginStateTTL)
			p.pending[providedState] = pending
		}
		p.mu.Unlock()
		if !isOAuthRequest {
			http.Error(w, "authorization request expired", http.StatusBadRequest)
			return
		}
		p.writeOAuthConsentPage(w, providedState, consentToken, pending)
		return
	}

	p.setSessionCookie(w, subject, expiresAt)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (p *Provider) HandleConsent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !p.localAuthorizationServer() {
		http.NotFound(w, r)
		return
	}
	if err := browserRequestProtection.Check(r); err != nil {
		http.Error(w, "요청 출처를 확인할 수 없습니다.", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid authorization decision", http.StatusBadRequest)
		return
	}
	state := strings.TrimSpace(r.PostFormValue("state"))
	consentToken := strings.TrimSpace(r.PostFormValue("consent_token"))
	decision := strings.TrimSpace(r.PostFormValue("decision"))
	if state == "" || len(state) > 128 || consentToken == "" || len(consentToken) > 128 || (decision != "allow" && decision != "deny") {
		http.Error(w, "invalid authorization decision", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	p.ensureOAuthMapsLocked()
	p.pruneOAuthStateLocked(time.Now())
	pending, ok := p.pending[state]
	if ok && pending.Subject != "" && len(consentToken) == len(pending.ConsentToken) && subtle.ConstantTimeCompare([]byte(consentToken), []byte(pending.ConsentToken)) == 1 {
		delete(p.pending, state)
	} else {
		ok = false
	}
	p.mu.Unlock()
	if !ok {
		http.Error(w, "authorization request expired", http.StatusBadRequest)
		return
	}
	if decision == "deny" {
		p.oauthError(w, r, pending.RedirectURI, pending.State, "access_denied", "the user denied the request", true)
		return
	}

	code, err := randomToken(32)
	if err != nil {
		p.oauthError(w, r, pending.RedirectURI, pending.State, "server_error", "could not create authorization code", true)
		return
	}
	p.mu.Lock()
	p.ensureOAuthMapsLocked()
	p.pruneOAuthStateLocked(time.Now())
	p.codes[code] = authorizationCode{
		Subject:         pending.Subject,
		ClientID:        pending.ClientID,
		RedirectURI:     pending.RedirectURI,
		CodeChallenge:   pending.CodeChallenge,
		ChallengeMethod: pending.ChallengeMethod,
		Resource:        pending.Resource,
		Scope:           pending.Scope,
		ExpiresAt:       time.Now().Add(authorizationCodeTTL),
	}
	p.mu.Unlock()
	p.setSessionCookie(w, pending.Subject, pending.SessionExpiresAt)
	values := url.Values{"code": {code}}
	if pending.State != "" {
		values.Set("state", pending.State)
	}
	p.oauthRedirect(w, r, pending.RedirectURI, values)
}

type authHTTPError struct {
	status  int
	message string
}

func (e authHTTPError) Error() string { return e.message }

func (p *Provider) completeOIDCLogin(r *http.Request, providedState string, w http.ResponseWriter) (string, time.Time, *authHTTPError) {
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" {
		return "", time.Time{}, &authHTTPError{http.StatusBadRequest, "login state is missing"}
	}
	nonceCookie, err := r.Cookie(nonceCookieName)
	if err != nil || nonceCookie.Value == "" {
		return "", time.Time{}, &authHTTPError{http.StatusBadRequest, "login nonce is missing"}
	}
	clearOAuthCookies(w, p.config.CookieSecure)
	if len(providedState) != len(stateCookie.Value) || subtle.ConstantTimeCompare([]byte(providedState), []byte(stateCookie.Value)) != 1 {
		return "", time.Time{}, &authHTTPError{http.StatusBadRequest, "invalid login state"}
	}
	if upstreamError := strings.TrimSpace(r.FormValue("error")); upstreamError != "" {
		return "", time.Time{}, &authHTTPError{http.StatusBadRequest, "login was denied: " + upstreamError}
	}
	code := r.FormValue("code")
	if code == "" {
		return "", time.Time{}, &authHTTPError{http.StatusBadRequest, "authorization code is missing"}
	}

	providerCtx, cancel := context.WithTimeout(r.Context(), oauthProviderTimeout)
	defer cancel()
	token, err := p.oauth2Config.Exchange(providerCtx, code)
	if err != nil {
		return "", time.Time{}, &authHTTPError{http.StatusBadGateway, "could not complete login"}
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return "", time.Time{}, &authHTTPError{http.StatusBadGateway, "identity token is missing"}
	}

	idToken, err := p.verifyIdentityToken(providerCtx, rawIDToken)
	if err != nil {
		// Some OAuth providers expose an opaque access token and publish no
		// usable JWKS for their ID token. The authorization-code exchange has
		// already authenticated the confidential client, so an active token
		// introspection result bound to that same client is a safe OAuth
		// compatibility fallback. Keep the verifier error in the log so the
		// provider can still be configured for normal OIDC validation later.
		if subject, expiresAt, introspectionErr := p.introspectLoginAccessToken(providerCtx, token.AccessToken); introspectionErr == nil {
			log.Printf("OIDC identity token verification failed; accepted introspected access token: %v", err)
			return subject, expiresAt, nil
		} else {
			log.Printf("OIDC identity token verification failed: %v (access-token introspection fallback failed: %v)", err, introspectionErr)
		}
		return "", time.Time{}, &authHTTPError{http.StatusUnauthorized, "identity token is invalid"}
	}
	if idToken.Nonce == "" || len(idToken.Nonce) != len(nonceCookie.Value) || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonceCookie.Value)) != 1 {
		return "", time.Time{}, &authHTTPError{http.StatusUnauthorized, "invalid login nonce"}
	}

	subject, err := subjectFromToken(idToken)
	if err != nil {
		return "", time.Time{}, &authHTTPError{http.StatusUnauthorized, "identity token has no stable subject"}
	}
	expiresAt := idToken.Expiry
	if expiresAt.IsZero() || !expiresAt.After(time.Now()) {
		return "", time.Time{}, &authHTTPError{http.StatusUnauthorized, "identity token is expired"}
	}
	return subject, expiresAt, nil
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

func loginStateMatches(r *http.Request, providedState string) bool {
	cookie, err := r.Cookie(stateCookieName)
	if err != nil || cookie.Value == "" || len(cookie.Value) != len(providedState) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(providedState)) == 1
}

func (p *Provider) setSessionCookie(w http.ResponseWriter, subject string, expiresAt time.Time) {
	if p.config.APISecret == "" {
		return
	}
	session, err := generateSessionToken(subject, p.config.APISecret, expiresAt)
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

func (p *Provider) clientRedirectAllowed(clientID, redirectURI string) bool {
	client, registered := p.client(clientID)
	if registered {
		for _, candidate := range client.RedirectURIs {
			if candidate == redirectURI {
				return true
			}
		}
		if len(client.RedirectURIs) == 0 && clientID == strings.TrimSpace(p.config.MCPClientID) && isLoopbackRedirect(redirectURI) {
			return true
		}
		return false
	}

	// A statically configured MCP client may use a loopback callback. This is
	// the normal native-client shape used by Codex and other local MCP clients.
	if clientID == strings.TrimSpace(p.config.MCPClientID) && isLoopbackRedirect(redirectURI) {
		return true
	}
	if clientID == strings.TrimSpace(p.config.ClientID) && redirectURI == strings.TrimSpace(p.config.RedirectURL) {
		return true
	}
	return false
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

func validPKCEChallenge(challenge string) bool {
	if len(challenge) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(decoded) == sha256.Size
}

func validPKCEVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, char := range verifier {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		switch char {
		case '-', '.', '_', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func validateSigningSecret(secret string) error {
	if len(strings.TrimSpace(secret)) < minimumSecretBytes {
		return fmt.Errorf("must contain at least %d bytes", minimumSecretBytes)
	}
	return nil
}

func normalizeResourceURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func (p *Provider) configuredResourceURL(r *http.Request) string {
	if configured := normalizeResourceURL(p.config.MCPResourceURL); configured != "" {
		return configured
	}
	return requestBaseURL(r) + "/mcp"
}

func (p *Provider) resourceAllowed(resource string, r *http.Request) bool {
	resource = normalizeResourceURL(resource)
	if resource == "" {
		return false
	}
	wanted := normalizeResourceURL(p.configuredResourceURL(r))
	return resource == wanted
}

func normalizeScope(_ string) string {
	// Stash only needs the stable subject from an ID token. Keep the upstream
	// request to the one scope this broker advertises, even when a client has
	// cached an older metadata response or asks for optional profile fields.
	// This also makes the client's no-scope retry genuinely scope-minimal.
	return oidc.ScopeOpenID
}

func mergeQuery(existing string, values url.Values) string {
	query, _ := url.ParseQuery(existing)
	for key, list := range values {
		query.Del(key)
		for _, value := range list {
			query.Add(key, value)
		}
	}
	return query.Encode()
}

func (p *Provider) oauthError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string, redirectAllowed bool) {
	if redirectAllowed && validRedirectURI(redirectURI) {
		values := url.Values{"error": {code}, "error_description": {description}}
		if state != "" {
			values.Set("state", state)
		}
		p.oauthRedirect(w, r, redirectURI, values)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

func (p *Provider) oauthRedirect(w http.ResponseWriter, r *http.Request, redirectURI string, values url.Values) {
	if !validRedirectURI(redirectURI) {
		http.Error(w, "invalid redirect URI", http.StatusBadRequest)
		return
	}
	location, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect URI", http.StatusBadRequest)
		return
	}
	if issuer := p.localIssuerURL(r); issuer != "" {
		values.Set("iss", issuer)
	}
	location.RawQuery = mergeQuery(location.RawQuery, values)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, location.String(), http.StatusFound)
}

func (p *Provider) allowOAuthRequest(rate *fixedWindowRateLimit, limit int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return rate.allow(time.Now(), limit)
}

func writeOAuthRateLimit(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(int(oauthRateWindow/time.Second)))
	writeOAuthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many OAuth requests")
}

func (p *Provider) pruneOAuthStateLocked(now time.Time) {
	for key, pending := range p.pending {
		if !pending.ExpiresAt.After(now) {
			delete(p.pending, key)
		}
	}
	for key, code := range p.codes {
		if !code.ExpiresAt.After(now) {
			delete(p.codes, key)
		}
	}
	for key, token := range p.refreshTokens {
		if !token.ExpiresAt.After(now) {
			delete(p.refreshTokens, key)
		}
	}
	for key, client := range p.clients {
		if client.Dynamic && now.Sub(client.LastUsed) >= dynamicClientIdleTTL && !p.clientInUseLocked(key) {
			delete(p.clients, key)
		}
	}
}

func (p *Provider) clientInUseLocked(clientID string) bool {
	for _, pending := range p.pending {
		if pending.ClientID == clientID {
			return true
		}
	}
	for _, code := range p.codes {
		if code.ClientID == clientID {
			return true
		}
	}
	for _, token := range p.refreshTokens {
		if token.ClientID == clientID {
			return true
		}
	}
	return false
}

func (p *Provider) evictOldestUnusedClientLocked() bool {
	oldestID := ""
	var oldest time.Time
	for clientID, client := range p.clients {
		if !client.Dynamic || p.clientInUseLocked(clientID) {
			continue
		}
		if oldestID == "" || client.LastUsed.Before(oldest) {
			oldestID = clientID
			oldest = client.LastUsed
		}
	}
	if oldestID == "" {
		return false
	}
	delete(p.clients, oldestID)
	return true
}

func (p *Provider) storeRefreshTokenLocked(key string, token refreshToken) {
	for p.refreshTokenCountLocked(token.Subject, token.ClientID, token.Resource) >= maxRefreshPerClient {
		p.deleteOldestRefreshTokenLocked(token.Subject, token.ClientID, token.Resource, true)
	}
	for len(p.refreshTokens) >= maxRefreshTokens {
		p.deleteOldestRefreshTokenLocked("", "", "", false)
	}
	p.refreshTokens[key] = token
}

func (p *Provider) refreshTokenCountLocked(subject, clientID, resource string) int {
	count := 0
	for _, token := range p.refreshTokens {
		if token.Subject == subject && token.ClientID == clientID && token.Resource == resource {
			count++
		}
	}
	return count
}

func (p *Provider) deleteOldestRefreshTokenLocked(subject, clientID, resource string, matchingOnly bool) {
	oldestKey := ""
	var oldest time.Time
	for key, token := range p.refreshTokens {
		if matchingOnly && (token.Subject != subject || token.ClientID != clientID || token.Resource != resource) {
			continue
		}
		if oldestKey == "" || token.CreatedAt.Before(oldest) {
			oldestKey = key
			oldest = token.CreatedAt
		}
	}
	if oldestKey != "" {
		delete(p.refreshTokens, oldestKey)
	}
}

func (p *Provider) ensureOAuthMapsLocked() {
	if p.clients == nil {
		p.clients = make(map[string]oauthClient)
	}
	if p.pending == nil {
		p.pending = make(map[string]authorizationRequest)
	}
	if p.codes == nil {
		p.codes = make(map[string]authorizationCode)
	}
	if p.refreshTokens == nil {
		p.refreshTokens = make(map[string]refreshToken)
	}
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
		if user, err := p.VerifyRequest(r); err == nil && user != "" {
			status["authenticated"] = true
			status["user"] = user
		}
	}
	_ = json.NewEncoder(w).Encode(status)
}

// HandleAuthorizationServerMetadata serves RFC 8414 metadata for the local
// OAuth broker. When no browser client is configured, the configured issuer
// remains the external authorization server and clients discover it directly.
func (p *Provider) HandleAuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !p.localAuthorizationServer() {
		http.NotFound(w, r)
		return
	}
	issuer := p.localIssuerURL(r)
	response := map[string]any{
		"issuer":                                         issuer,
		"authorization_endpoint":                         issuer + "/authorize",
		"token_endpoint":                                 issuer + "/oauth/token",
		"registration_endpoint":                          issuer + "/oauth/register",
		"scopes_supported":                               []string{oidc.ScopeOpenID},
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_post", "client_secret_basic"},
		"authorization_response_iss_parameter_supported": true,
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// HandleProtectedResourceMetadata serves RFC 9728 metadata for Codex and
// other MCP OAuth clients. It points either to Stash's local OAuth broker or
// to the configured external authorization server.
func (p *Provider) HandleProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if p == nil || strings.TrimSpace(p.config.Issuer) == "" {
		http.NotFound(w, r)
		return
	}

	response := map[string]any{
		"resource":              p.resourceURLForMetadata(r),
		"authorization_servers": []string{p.authorizationServerURL(r)},
		"scopes_supported":      []string{oidc.ScopeOpenID},
		"resource_name":         "Stash Memory MCP",
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (p *Provider) authorizationServerURL(r *http.Request) string {
	if p.localAuthorizationServer() {
		return p.localIssuerURL(r)
	}
	return strings.TrimSpace(p.config.Issuer)
}

func (p *Provider) localIssuerURL(r *http.Request) string {
	base := requestBaseURL(r)
	if configured := strings.TrimSpace(p.config.MCPResourceURL); configured != "" {
		if parsed, err := url.Parse(configured); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			base = parsed.Scheme + "://" + parsed.Host
		}
	}
	return strings.TrimRight(base, "/")
}

// MCPUnauthorized writes the RFC 6750 challenge that points an OAuth client
// to the protected-resource metadata for the endpoint it requested.
func (p *Provider) MCPUnauthorized(w http.ResponseWriter, r *http.Request) {
	if !p.oauthMode() {
		w.Header().Set("WWW-Authenticate", `Bearer realm="stash"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	metadataURL := p.protectedResourceMetadataURL(r)
	if metadataURL == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="stash"`)
	} else {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+metadataURL+`"`)
	}
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// VerifyRequest validates a signed Stash session/API token or an OIDC access
// token sent as a bearer credential. It is used by the browser and maintenance
// routes, where the OIDC session remains the login boundary.
func (p *Provider) VerifyRequest(r *http.Request) (string, error) {
	return p.verifyRequest(r, false)
}

// VerifyMCPRequest validates the credential used by MCP transports. It accepts
// either a Stash API token or a Stash OAuth access token whose resource and
// signature are checked locally. A browser session cookie is retained only for
// the embedded console, which already runs on the same origin.
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
		if mcp && bearer {
			return "", errors.New("MCP OAuth or API token required")
		}
		return parseSessionToken(rawToken, p.config.APISecret)
	}
	if strings.HasPrefix(rawToken, oauthTokenPrefix) {
		return parseOAuthAccessToken(rawToken, p.config.APISecret, p.configuredResourceURL(r))
	}
	if strings.HasPrefix(rawToken, apiTokenPrefix) {
		return parseStashToken(rawToken, p.config.APISecret)
	}
	if mcp {
		return "", errors.New("MCP OAuth or API token required")
	}
	if !bearer {
		return "", errors.New("unsupported session credential")
	}
	return p.verifyOIDCAccessToken(r, rawToken)
}

// VerifyBearerToken validates a bearer credential supplied to the STDIO
// adapter through STASH_AUTH_STDIO_TOKEN. It deliberately accepts only tokens
// that Stash can validate locally; STDIO has no HTTP request metadata for an
// external issuer discovery flow.
func (p *Provider) VerifyBearerToken(ctx context.Context, rawToken string) (string, error) {
	if p == nil {
		return "", errors.New("authentication is disabled")
	}
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return "", errors.New("missing authentication token")
	}
	if strings.HasPrefix(rawToken, oauthTokenPrefix) {
		return parseOAuthAccessToken(rawToken, p.config.APISecret, normalizeResourceURL(p.config.MCPResourceURL))
	}
	if strings.HasPrefix(rawToken, apiTokenPrefix) {
		return parseStashToken(rawToken, p.config.APISecret)
	}
	if strings.HasPrefix(rawToken, sessionTokenPrefix) {
		return parseSessionToken(rawToken, p.config.APISecret)
	}
	if p.oauthMode() {
		return p.verifyOIDCAccessTokenContext(ctx, rawToken)
	}
	return "", errors.New("unsupported STDIO credential")
}

func (p *Provider) verifyOIDCAccessToken(r *http.Request, rawToken string) (string, error) {
	return p.verifyOIDCAccessTokenContext(r.Context(), rawToken)
}

func (p *Provider) verifyOIDCAccessTokenContext(ctx context.Context, rawToken string) (string, error) {
	if p.accessVerifier == nil {
		return "", errors.New("OIDC access-token verifier is unavailable")
	}
	providerCtx, cancel := context.WithTimeout(ctx, oauthProviderTimeout)
	defer cancel()
	accessToken, err := p.verifyAccessToken(providerCtx, rawToken)
	if err != nil {
		if subject, introspectionErr := p.introspectAccessToken(providerCtx, rawToken); introspectionErr == nil {
			return subject, nil
		}
		return "", fmt.Errorf("invalid OIDC access token: %w", err)
	}
	if !p.accessTokenAudienceAllowed(accessToken.Audience) {
		return "", fmt.Errorf("OIDC access token has unexpected audience %q", accessToken.Audience)
	}
	return subjectFromToken(accessToken)
}

func (p *Provider) verifyAccessToken(ctx context.Context, rawToken string) (*oidc.IDToken, error) {
	if p.hmacAccessVerifier != nil {
		if token, err := p.hmacAccessVerifier.Verify(ctx, rawToken); err == nil {
			return token, nil
		}
	}
	if p.accessVerifier == nil {
		return nil, errors.New("OIDC access-token verifier is unavailable")
	}
	return p.accessVerifier.Verify(ctx, rawToken)
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

func (p *Provider) introspectAccessToken(ctx context.Context, rawToken string) (string, error) {
	if p.introspectionEndpoint == "" || p.config.ClientID == "" || p.config.ClientSecret == "" {
		return "", errors.New("OIDC token introspection is not configured")
	}
	form := url.Values{"token": {rawToken}, "token_type_hint": {"access_token"}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.introspectionEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(p.config.ClientID, p.config.ClientSecret)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("introspection returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Active bool            `json:"active"`
		Sub    string          `json:"sub"`
		Exp    int64           `json:"exp"`
		Aud    json.RawMessage `json:"aud"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 256<<10)).Decode(&result); err != nil {
		return "", err
	}
	if !result.Active || result.Sub == "" || (result.Exp > 0 && result.Exp <= time.Now().Unix()) {
		return "", errors.New("inactive OIDC access token")
	}
	audience, err := decodeAudience(result.Aud)
	if err != nil || !p.accessTokenAudienceAllowed(audience) {
		return "", errors.New("OIDC access token has unexpected audience")
	}
	return result.Sub, nil
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

func (p *Provider) accessTokenAudienceAllowed(audience []string) bool {
	expected := make([]string, 0, 2)
	if clientID := strings.TrimSpace(p.config.MCPClientID); clientID != "" {
		expected = append(expected, clientID)
	}
	if resourceURL := strings.TrimRight(strings.TrimSpace(p.config.MCPResourceURL), "/"); resourceURL != "" {
		expected = append(expected, resourceURL)
	}
	for _, wanted := range expected {
		if audienceContains(audience, wanted) {
			return true
		}
	}
	return false
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

func (p *Provider) resourceURLForMetadata(r *http.Request) string {
	if configured := strings.TrimRight(strings.TrimSpace(p.config.MCPResourceURL), "/"); configured != "" {
		return configured
	}
	return requestBaseURL(r) + metadataResourcePath(r.URL.Path)
}

func (p *Provider) protectedResourceMetadataURL(r *http.Request) string {
	base := requestBaseURL(r)
	if p != nil {
		if configured := strings.TrimSpace(p.config.MCPResourceURL); configured != "" {
			if parsed, err := url.Parse(configured); err == nil && parsed.Scheme != "" && parsed.Host != "" {
				base = parsed.Scheme + "://" + parsed.Host
			}
		}
	}
	if base == "" {
		return ""
	}
	return base + "/.well-known/oauth-protected-resource" + metadataResourcePath(r.URL.Path)
}

func metadataResourcePath(path string) string {
	const prefix = "/.well-known/oauth-protected-resource"
	if strings.HasPrefix(path, prefix) {
		path = strings.TrimPrefix(path, prefix)
	}
	if path == "" || path == "/" {
		return "/mcp"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func requestBaseURL(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if strings.TrimSpace(r.Host) == "" {
		return ""
	}
	return (&url.URL{Scheme: scheme, Host: strings.TrimSpace(r.Host)}).String()
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
	user, err := p.VerifyRequest(r)
	if err != nil || user == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	ttl := p.config.APITokenTTL
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	token, err := generateStashToken(user, p.config.APISecret, ttl)
	if err != nil {
		http.Error(w, `{"error":"token generation is unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token":      token,
		"token_type": "Bearer",
		"expires_in": int64(ttl / time.Second),
	})
}

// HandleOAuthToken implements the authorization_code and refresh_token grants
// used by HTTP MCP clients. The access token is opaque, but is signed by Stash
// and carries the MCP resource in its protected payload.
func (p *Provider) HandleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "token endpoint requires POST")
		return
	}
	if !p.localAuthorizationServer() {
		writeOAuthError(w, http.StatusNotImplemented, "temporarily_unavailable", "local OAuth token endpoint is not configured")
		return
	}
	if !p.allowOAuthRequest(&p.tokenRate, maxTokenRequests) {
		writeOAuthRateLimit(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "could not parse form body")
		return
	}
	grantType := strings.TrimSpace(r.Form.Get("grant_type"))
	clientID, clientSecret, fromBasic := oauthClientCredentials(r)
	if clientID == "" || len(clientID) > 256 || len(clientSecret) > 256 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "client_id is required")
		return
	}
	client, ok := p.client(clientID)
	if !ok {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "unknown client_id")
		return
	}
	if !oauthClientAuthenticated(client, clientSecret, fromBasic) {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}

	switch grantType {
	case "authorization_code":
		p.exchangeAuthorizationCode(w, r, clientID)
	case "refresh_token":
		p.exchangeRefreshToken(w, r, clientID)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

func (p *Provider) exchangeAuthorizationCode(w http.ResponseWriter, r *http.Request, clientID string) {
	codeValue := strings.TrimSpace(r.Form.Get("code"))
	redirectURI := strings.TrimSpace(r.Form.Get("redirect_uri"))
	verifier := strings.TrimSpace(r.Form.Get("code_verifier"))
	if codeValue == "" || redirectURI == "" || verifier == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code, redirect_uri, and code_verifier are required")
		return
	}
	if !validPKCEVerifier(verifier) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code_verifier must be 43 to 128 unreserved characters")
		return
	}

	p.mu.Lock()
	p.ensureOAuthMapsLocked()
	p.pruneOAuthStateLocked(time.Now())
	code, ok := p.codes[codeValue]
	if ok {
		delete(p.codes, codeValue)
	}
	p.mu.Unlock()
	if !ok || !code.ExpiresAt.After(time.Now()) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid or expired")
		return
	}
	if code.ClientID != clientID || code.RedirectURI != redirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code does not match the client")
		return
	}
	if code.ChallengeMethod != "S256" || !pkceMatches(code.CodeChallenge, verifier) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match the authorization request")
		return
	}
	resource := normalizeResourceURL(r.Form.Get("resource"))
	if resource == "" || resource != normalizeResourceURL(code.Resource) || !p.resourceAllowed(resource, r) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource does not match the authorization request")
		return
	}
	p.issueOAuthTokens(w, code.Subject, clientID, resource, code.Scope)
}

func (p *Provider) exchangeRefreshToken(w http.ResponseWriter, r *http.Request, clientID string) {
	provided := strings.TrimSpace(r.Form.Get("refresh_token"))
	if provided == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	p.mu.Lock()
	p.ensureOAuthMapsLocked()
	p.pruneOAuthStateLocked(time.Now())
	refresh, ok := p.refreshTokens[provided]
	p.mu.Unlock()
	if !ok || refresh.ClientID != clientID || !refresh.ExpiresAt.After(time.Now()) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh_token is invalid or expired")
		return
	}
	resource := normalizeResourceURL(r.Form.Get("resource"))
	if resource == "" || resource != normalizeResourceURL(refresh.Resource) || !p.resourceAllowed(resource, r) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource does not match the refresh token")
		return
	}
	p.mu.Lock()
	if _, stillValid := p.refreshTokens[provided]; !stillValid {
		p.mu.Unlock()
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh_token is invalid or expired")
		return
	}
	// Public clients get refresh-token rotation: the old token cannot be
	// replayed after this request.
	delete(p.refreshTokens, provided)
	p.mu.Unlock()
	p.issueOAuthTokens(w, refresh.Subject, clientID, resource, refresh.Scope)
}

func (p *Provider) issueOAuthTokens(w http.ResponseWriter, subject, clientID, resource, scope string) {
	accessTTL := p.config.AccessTokenTTL
	if accessTTL <= 0 {
		accessTTL = defaultAccessTokenTTL
	}
	refreshTTL := p.config.RefreshTokenTTL
	if refreshTTL <= 0 {
		refreshTTL = defaultRefreshTokenTTL
	}
	access, err := generateOAuthAccessToken(subject, p.config.APISecret, resource, scope, accessTTL)
	if err != nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "access token generation is unavailable")
		return
	}
	rawRefresh, err := randomToken(48)
	if err != nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "refresh token generation is unavailable")
		return
	}
	refresh := refreshTokenPrefix + rawRefresh
	p.mu.Lock()
	p.ensureOAuthMapsLocked()
	p.pruneOAuthStateLocked(time.Now())
	p.storeRefreshTokenLocked(refresh, refreshToken{
		Subject:   subject,
		ClientID:  clientID,
		Resource:  resource,
		Scope:     scope,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(refreshTTL),
	})
	p.mu.Unlock()

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(accessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         scope,
	})
}

// HandleOAuthRegister implements the RFC 7591 client-registration subset used
// by MCP clients. Redirect URIs are retained in memory until the process
// restarts; clients can register again after a restart.
func (p *Provider) HandleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "registration endpoint requires POST")
		return
	}
	if !p.localAuthorizationServer() {
		http.NotFound(w, r)
		return
	}
	if !p.allowOAuthRequest(&p.registerRate, maxRegisterRequests) {
		writeOAuthRateLimit(w)
		return
	}
	defer r.Body.Close()
	var request struct {
		RedirectURIs            []string `json:"redirect_uris"`
		ClientName              string   `json:"client_name"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		GrantTypes              []string `json:"grant_types"`
		ResponseTypes           []string `json:"response_types"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := decoder.Decode(&request); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "registration body is invalid")
		return
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "registration body must contain one JSON object")
		return
	}
	request.ClientName = strings.TrimSpace(request.ClientName)
	if len(request.ClientName) > 128 || !utf8.ValidString(request.ClientName) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name must be at most 128 bytes")
		return
	}
	if len(request.RedirectURIs) == 0 || len(request.RedirectURIs) > 4 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris is required")
		return
	}
	for _, redirectURI := range request.RedirectURIs {
		if len(redirectURI) > 2048 || !validRedirectURI(redirectURI) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must use HTTPS or loopback HTTP")
			return
		}
	}
	if request.TokenEndpointAuthMethod != "" && request.TokenEndpointAuthMethod != "none" && request.TokenEndpointAuthMethod != "client_secret_post" && request.TokenEndpointAuthMethod != "client_secret_basic" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token endpoint authentication method")
		return
	}
	for _, grantType := range request.GrantTypes {
		if grantType != "authorization_code" && grantType != "refresh_token" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only authorization_code and refresh_token are supported")
			return
		}
	}
	for _, responseType := range request.ResponseTypes {
		if responseType != "code" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "only code response type is supported")
			return
		}
	}
	clientID, err := randomToken(24)
	if err != nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "client registration is unavailable")
		return
	}
	clientID = "stash_" + clientID
	method := request.TokenEndpointAuthMethod
	secret := ""
	if method == "" {
		method = "none"
	}
	if method != "none" {
		rawSecret, err := randomToken(32)
		if err != nil {
			writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "client registration is unavailable")
			return
		}
		secret = rawSecret
	}
	now := time.Now()
	client := oauthClient{
		ID:                      clientID,
		RedirectURIs:            append([]string(nil), request.RedirectURIs...),
		TokenEndpointAuthMethod: method,
		Secret:                  secret,
		Name:                    request.ClientName,
		Dynamic:                 true,
		LastUsed:                now,
	}
	p.mu.Lock()
	p.ensureOAuthMapsLocked()
	p.pruneOAuthStateLocked(now)
	if len(p.clients) >= maxOAuthClients && !p.evictOldestUnusedClientLocked() {
		p.mu.Unlock()
		writeOAuthError(w, http.StatusServiceUnavailable, "temporarily_unavailable", "client registration limit reached")
		return
	}
	p.clients[clientID] = client
	p.mu.Unlock()

	if len(request.GrantTypes) == 0 {
		request.GrantTypes = []string{"authorization_code", "refresh_token"}
	}
	if len(request.ResponseTypes) == 0 {
		request.ResponseTypes = []string{"code"}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	response := map[string]any{
		"client_id":                  clientID,
		"client_id_issued_at":        time.Now().Unix(),
		"client_name":                request.ClientName,
		"redirect_uris":              request.RedirectURIs,
		"grant_types":                request.GrantTypes,
		"response_types":             request.ResponseTypes,
		"token_endpoint_auth_method": method,
	}
	if secret != "" {
		// RFC 7591 returns the secret only at registration time.
		response["client_secret"] = secret
	}
	_ = json.NewEncoder(w).Encode(response)
}

func (p *Provider) client(clientID string) (oauthClient, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureOAuthMapsLocked()
	p.pruneOAuthStateLocked(time.Now())
	if client, ok := p.clients[clientID]; ok {
		if client.Dynamic {
			client.LastUsed = time.Now()
			p.clients[clientID] = client
		}
		return client, true
	}
	if clientID == strings.TrimSpace(p.config.MCPClientID) {
		return oauthClient{ID: clientID, TokenEndpointAuthMethod: "none"}, true
	}
	return oauthClient{}, false
}

func (p *Provider) clientExists(clientID string) bool {
	_, ok := p.client(clientID)
	return ok
}

func oauthClientCredentials(r *http.Request) (clientID, clientSecret string, fromBasic bool) {
	if id, secret, ok := r.BasicAuth(); ok {
		return id, secret, true
	}
	return strings.TrimSpace(r.Form.Get("client_id")), strings.TrimSpace(r.Form.Get("client_secret")), false
}

func oauthClientAuthenticated(client oauthClient, secret string, fromBasic bool) bool {
	switch client.TokenEndpointAuthMethod {
	case "", "none":
		return !fromBasic && secret == ""
	case "client_secret_post":
		if fromBasic || secret == "" || client.Secret == "" {
			return false
		}
	case "client_secret_basic":
		if !fromBasic || secret == "" || client.Secret == "" {
			return false
		}
	default:
		return false
	}
	return len(secret) == len(client.Secret) && subtle.ConstantTimeCompare([]byte(secret), []byte(client.Secret)) == 1
}

func pkceMatches(challenge, verifier string) bool {
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return len(challenge) == len(computed) && subtle.ConstantTimeCompare([]byte(challenge), []byte(computed)) == 1
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
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

// GenerateAPIToken creates a Stash bearer token without contacting an OIDC
// provider. The caller must keep the returned value private.
func GenerateAPIToken(user, secret string, ttl time.Duration) (string, error) {
	if err := validateSigningSecret(secret); err != nil {
		return "", fmt.Errorf("API secret %w", err)
	}
	return generateStashToken(user, secret, ttl)
}

func generateStashToken(user, secret string, ttl time.Duration) (string, error) {
	if user == "" {
		return "", errors.New("user is required")
	}
	if secret == "" {
		return "", errors.New("API secret is required")
	}
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}
	nonce, err := randomToken(24)
	if err != nil {
		return "", err
	}
	payload := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte(user)),
		strconv.FormatInt(time.Now().Add(ttl).Unix(), 10),
		nonce,
	}, ".")
	signature := sign(payload, secret)
	return apiTokenPrefix + payload + "." + signature, nil
}

func generateOAuthAccessToken(user, secret, resource, scope string, ttl time.Duration) (string, error) {
	if user == "" {
		return "", errors.New("user is required")
	}
	if secret == "" {
		return "", errors.New("API secret is required")
	}
	if normalizeResourceURL(resource) == "" {
		return "", errors.New("resource is required")
	}
	if ttl <= 0 {
		ttl = defaultAccessTokenTTL
	}
	nonce, err := randomToken(24)
	if err != nil {
		return "", err
	}
	payload := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte(user)),
		strconv.FormatInt(time.Now().Add(ttl).Unix(), 10),
		base64.RawURLEncoding.EncodeToString([]byte(normalizeResourceURL(resource))),
		base64.RawURLEncoding.EncodeToString([]byte(scope)),
		nonce,
	}, ".")
	return oauthTokenPrefix + payload + "." + sign(payload, secret), nil
}

func generateSessionToken(user, secret string, expiresAt time.Time) (string, error) {
	if user == "" {
		return "", errors.New("user is required")
	}
	if secret == "" {
		return "", errors.New("API secret is required")
	}
	if !expiresAt.After(time.Now()) {
		return "", errors.New("session expiry must be in the future")
	}
	payload := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte(user)),
		strconv.FormatInt(expiresAt.Unix(), 10),
	}, ".")
	return sessionTokenPrefix + payload + "." + sign(payload, secret), nil
}

func parseSessionToken(token, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("API secret is not configured")
	}
	if !strings.HasPrefix(token, sessionTokenPrefix) {
		return "", errors.New("invalid session token prefix")
	}
	parts := strings.Split(strings.TrimPrefix(token, sessionTokenPrefix), ".")
	if len(parts) != 3 {
		return "", errors.New("invalid session token format")
	}
	payload := strings.Join(parts[:2], ".")
	expected, _ := hex.DecodeString(sign(payload, secret))
	provided, err := hex.DecodeString(parts[2])
	if err != nil || subtle.ConstantTimeCompare(provided, expected) != 1 {
		return "", errors.New("invalid session token signature")
	}
	decodedUser, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(decodedUser) == 0 {
		return "", errors.New("invalid session token user")
	}
	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expiresAt <= time.Now().Unix() {
		return "", errors.New("session token expired")
	}
	return string(decodedUser), nil
}

func parseOAuthAccessToken(token, secret, expectedResource string) (string, error) {
	if secret == "" {
		return "", errors.New("API secret is not configured")
	}
	if !strings.HasPrefix(token, oauthTokenPrefix) {
		return "", errors.New("invalid OAuth access token prefix")
	}
	parts := strings.Split(strings.TrimPrefix(token, oauthTokenPrefix), ".")
	if len(parts) != 6 {
		return "", errors.New("invalid OAuth access token format")
	}
	payload := strings.Join(parts[:5], ".")
	expected, err := hex.DecodeString(sign(payload, secret))
	if err != nil {
		return "", errors.New("invalid OAuth access token signature")
	}
	provided, err := hex.DecodeString(parts[5])
	if err != nil || subtle.ConstantTimeCompare(provided, expected) != 1 {
		return "", errors.New("invalid OAuth access token signature")
	}
	decodedUser, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(decodedUser) == 0 {
		return "", errors.New("invalid OAuth access token user")
	}
	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expiresAt <= time.Now().Unix() {
		return "", errors.New("OAuth access token expired")
	}
	resourceBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || normalizeResourceURL(string(resourceBytes)) == "" {
		return "", errors.New("invalid OAuth access token resource")
	}
	if expectedResource = normalizeResourceURL(expectedResource); expectedResource != "" && normalizeResourceURL(string(resourceBytes)) != expectedResource {
		return "", errors.New("OAuth access token has unexpected resource")
	}
	scopeBytes, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || string(scopeBytes) != oidc.ScopeOpenID {
		return "", errors.New("invalid OAuth access token scope")
	}
	return string(decodedUser), nil
}

func parseStashToken(token, secret string) (string, error) {
	user, _, err := parseStashTokenClaims(token, secret)
	return user, err
}

func parseStashTokenClaims(token, secret string) (string, time.Time, error) {
	if secret == "" {
		return "", time.Time{}, errors.New("API secret is not configured")
	}
	if !strings.HasPrefix(token, apiTokenPrefix) {
		return "", time.Time{}, errors.New("invalid API token prefix")
	}
	parts := strings.Split(strings.TrimPrefix(token, apiTokenPrefix), ".")
	if len(parts) != 4 {
		return "", time.Time{}, errors.New("invalid API token format")
	}
	payload := strings.Join(parts[:3], ".")
	expected, err := hex.DecodeString(sign(payload, secret))
	if err != nil {
		return "", time.Time{}, errors.New("invalid API token signature")
	}
	provided, err := hex.DecodeString(parts[3])
	if err != nil || subtle.ConstantTimeCompare(provided, expected) != 1 {
		return "", time.Time{}, errors.New("invalid API token signature")
	}

	decodedUser, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(decodedUser) == 0 {
		return "", time.Time{}, errors.New("invalid API token user")
	}
	expiresUnix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expiresUnix <= time.Now().Unix() {
		return "", time.Time{}, errors.New("API token expired")
	}
	return string(decodedUser), time.Unix(expiresUnix, 0), nil
}

func sign(payload, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}
