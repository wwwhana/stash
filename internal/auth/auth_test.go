package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

const (
	testSigningSecret = "0123456789abcdef0123456789abcdef"
	testPKCEVerifier  = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFG"
)

func TestStashTokenRoundTrip(t *testing.T) {
	token, err := generateStashToken("subject-1", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	got, err := parseStashToken(token, "test-secret")
	if err != nil {
		t.Fatalf("parse token: %v", err)
	}
	if got != "subject-1" {
		t.Fatalf("user = %q, want subject-1", got)
	}
}

func TestStashTokenRejectsTamperingAndWrongSecret(t *testing.T) {
	token, err := generateStashToken("subject-1", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	parts := strings.Split(token, ".")
	parts[1] = strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	if _, err := parseStashToken(strings.Join(parts, "."), "test-secret"); err == nil {
		t.Fatal("tampered token was accepted")
	}
	if _, err := parseStashToken(token, "wrong-secret"); err == nil {
		t.Fatal("token signed by another secret was accepted")
	}
}

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

func TestTokenLoginSetsSessionCookie(t *testing.T) {
	p := &Provider{config: Config{Mode: "token", APISecret: testSigningSecret}}
	token, err := GenerateAPIToken("subject-1", testSigningSecret, time.Hour)
	if err != nil {
		t.Fatalf("generate API token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(url.Values{"token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	p.HandleLogin(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("token login status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	setCookie := rec.Result().Cookies()
	if len(setCookie) != 1 || setCookie[0].Name != sessionCookieName || setCookie[0].Value == "" {
		t.Fatalf("token login cookies = %#v", setCookie)
	}
	verify := httptest.NewRequest(http.MethodGet, "/", nil)
	verify.AddCookie(setCookie[0])
	if user, err := p.VerifyRequest(verify); err != nil || user != "subject-1" {
		t.Fatalf("token login session = %q, %v", user, err)
	}

	queryToken := httptest.NewRecorder()
	queryRequest := httptest.NewRequest(http.MethodPost, "/auth/login?token="+url.QueryEscape(token), strings.NewReader(""))
	queryRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	p.HandleLogin(queryToken, queryRequest)
	if queryToken.Code != http.StatusUnauthorized || len(queryToken.Result().Cookies()) != 0 {
		t.Fatalf("query token login status=%d cookies=%#v", queryToken.Code, queryToken.Result().Cookies())
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
	token, err := GenerateAPIToken("subject-1", testSigningSecret, time.Hour)
	if err != nil {
		t.Fatalf("generate API token: %v", err)
	}
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
	if _, err := GenerateAPIToken("subject-1", "short", time.Hour); err == nil {
		t.Fatal("API token generation accepted a short signing secret")
	}
}

func TestOAuthConfigurationFailsBeforeProviderDiscovery(t *testing.T) {
	base := Config{
		Mode:            "oauth",
		Issuer:          "https://auth.example.com/",
		APISecret:       testSigningSecret,
		MCPResourceURL:  "https://stash.example.com/mcp",
		AccessTokenTTL:  time.Hour,
		RefreshTokenTTL: 24 * time.Hour,
	}
	missingResource := base
	missingResource.MCPResourceURL = ""
	if _, err := Init(context.Background(), missingResource); err == nil || !strings.Contains(err.Error(), "MCP_RESOURCE_URL") {
		t.Fatalf("missing resource URL error = %v", err)
	}
	weakSecret := base
	weakSecret.APISecret = "short"
	if _, err := Init(context.Background(), weakSecret); err == nil || !strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("weak OAuth secret error = %v", err)
	}
	longAccess := base
	longAccess.AccessTokenTTL = time.Hour + time.Second
	if _, err := Init(context.Background(), longAccess); err == nil || !strings.Contains(err.Error(), "must not exceed 1h") {
		t.Fatalf("long access-token lifetime error = %v", err)
	}
	insecureIssuer := base
	insecureIssuer.Issuer = "http://auth.example.com/"
	if _, err := Init(context.Background(), insecureIssuer); err == nil || !strings.Contains(err.Error(), "issuer must use HTTPS") {
		t.Fatalf("insecure issuer error = %v", err)
	}
	insecureRedirect := base
	insecureRedirect.ClientID = "browser-client"
	insecureRedirect.ClientSecret = "browser-secret"
	insecureRedirect.RedirectURL = "http://stash.example.com/auth/callback"
	if _, err := Init(context.Background(), insecureRedirect); err == nil || !strings.Contains(err.Error(), "redirect URL must use HTTPS") {
		t.Fatalf("insecure redirect error = %v", err)
	}
	insecureCookie := base
	insecureCookie.ClientID = "browser-client"
	insecureCookie.ClientSecret = "browser-secret"
	insecureCookie.RedirectURL = "https://stash.example.com/auth/callback"
	if _, err := Init(context.Background(), insecureCookie); err == nil || !strings.Contains(err.Error(), "requires secure cookies") {
		t.Fatalf("insecure cookie error = %v", err)
	}
}

func TestOAuthLoginDefaultsToBrowserRedirect(t *testing.T) {
	p := newLocalOAuthProvider()
	p.oauth2Config.Endpoint.AuthURL = "https://auth.example.com/application/o/authorize/"
	rec := httptest.NewRecorder()
	p.HandleLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("OAuth login status = %d, body = %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, p.oauth2Config.Endpoint.AuthURL) || !strings.Contains(location, "client_id=authentik-stash") || !strings.Contains(location, "state=") {
		t.Fatalf("OAuth login location = %q", location)
	}
}

func TestOAuthLoginCanUseTokenFormExplicitly(t *testing.T) {
	p := newLocalOAuthProvider()
	p.oauth2Config.Endpoint.AuthURL = "https://auth.example.com/application/o/authorize/"
	rec := httptest.NewRecorder()
	p.HandleLogin(rec, httptest.NewRequest(http.MethodGet, "/auth/login?provider=token", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="token"`) || !strings.Contains(rec.Body.String(), `provider=oidc`) {
		t.Fatalf("explicit token login status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGenerateTokenReportsBearerExpiry(t *testing.T) {
	p := &Provider{config: Config{APISecret: "test-secret", APITokenTTL: 2 * time.Hour}}
	session, err := generateSessionToken("subject-1", "test-secret", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("generate session: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/auth/token", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	rec := httptest.NewRecorder()
	p.HandleGenerateToken(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Token     string `json:"token"`
		TokenType string `json:"token_type"`
		ExpiresIn int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Token == "" || body.TokenType != "Bearer" || body.ExpiresIn != int64((2*time.Hour)/time.Second) {
		t.Fatalf("unexpected token response: %#v", body)
	}
	if user, err := p.VerifyRequest(httptest.NewRequest(http.MethodGet, "/", nil)); err == nil || user != "" {
		t.Fatalf("missing credential unexpectedly verified: %q, %v", user, err)
	}
	verify := httptest.NewRequest(http.MethodGet, "/", nil)
	verify.Header.Set("Authorization", "Bearer "+body.Token)
	if user, err := p.VerifyRequest(verify); err != nil || user != "subject-1" {
		t.Fatalf("issued token did not verify: %q, %v", user, err)
	}
}

func TestVerifyMCPRequestAcceptsStashAndOAuthTokens(t *testing.T) {
	p := &Provider{config: Config{
		APISecret:      "test-secret",
		MCPResourceURL: "https://stash.example.com/mcp",
	}}
	apiToken, err := generateStashToken("subject-1", "test-secret", time.Hour)
	if err != nil {
		t.Fatalf("generate API token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	if got, err := p.VerifyMCPRequest(req); err != nil || got != "subject-1" {
		t.Fatalf("native MCP token = %q, %v", got, err)
	}

	oauthToken, err := generateOAuthAccessToken("subject-1", "test-secret", "https://stash.example.com/mcp", "openid", time.Hour)
	if err != nil {
		t.Fatalf("generate OAuth token: %v", err)
	}
	oauth := httptest.NewRequest(http.MethodPost, "https://stash.example.com/mcp", nil)
	oauth.Header.Set("Authorization", "Bearer "+oauthToken)
	if got, err := p.VerifyMCPRequest(oauth); err != nil || got != "subject-1" {
		t.Fatalf("OAuth MCP token = %q, %v", got, err)
	}

	wrongResource, err := generateOAuthAccessToken("subject-1", "test-secret", "https://other.example.com/mcp", "openid", time.Hour)
	if err != nil {
		t.Fatalf("generate wrong-resource OAuth token: %v", err)
	}
	wrong := httptest.NewRequest(http.MethodPost, "https://stash.example.com/mcp", nil)
	wrong.Header.Set("Authorization", "Bearer "+wrongResource)
	if got, err := p.VerifyMCPRequest(wrong); err == nil || got != "" || !strings.Contains(err.Error(), "unexpected resource") {
		t.Fatalf("wrong-resource OAuth token was accepted: %q, %v", got, err)
	}

	oidc := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	oidc.Header.Set("Authorization", "Bearer upstream-oidc-token")
	if got, err := p.VerifyMCPRequest(oidc); err == nil || got != "" || !strings.Contains(err.Error(), "MCP OAuth or API token required") {
		t.Fatalf("OIDC token was accepted or returned the wrong error: %q, %v", got, err)
	}

	session, err := generateSessionToken("subject-1", "test-secret", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("generate session: %v", err)
	}
	cookie := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	cookie.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	if got, err := p.VerifyMCPRequest(cookie); err != nil || got != "subject-1" {
		t.Fatalf("browser session = %q, %v", got, err)
	}
}

func TestGenerateAPITokenDoesNotRequireOIDC(t *testing.T) {
	token, err := GenerateAPIToken("agent-1", testSigningSecret, time.Hour)
	if err != nil {
		t.Fatalf("generate API token: %v", err)
	}
	if got, err := parseStashToken(token, testSigningSecret); err != nil || got != "agent-1" {
		t.Fatalf("generated API token = %q, %v", got, err)
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

func newLocalOAuthProvider() *Provider {
	return &Provider{
		config: Config{
			Mode:           "oauth",
			APISecret:      "test-secret",
			MCPClientID:    "codex",
			MCPResourceURL: "https://stash.example.com/mcp",
			APITokenTTL:    time.Hour,
		},
		oauth2Config: oauth2.Config{ClientID: "authentik-stash"},
		verifier:     &oidc.IDTokenVerifier{},
		clients: map[string]oauthClient{
			"codex": {ID: "codex", TokenEndpointAuthMethod: "none"},
		},
		pending:       make(map[string]authorizationRequest),
		codes:         make(map[string]authorizationCode),
		refreshTokens: make(map[string]refreshToken),
	}
}

func TestOAuthAccessTokenCarriesResourceAndExpires(t *testing.T) {
	token, err := generateOAuthAccessToken("subject-1", "test-secret", "https://stash.example.com/mcp", "openid", time.Hour)
	if err != nil {
		t.Fatalf("generate OAuth token: %v", err)
	}
	if got, err := parseOAuthAccessToken(token, "test-secret", "https://stash.example.com/mcp/"); err != nil || got != "subject-1" {
		t.Fatalf("parse OAuth token = %q, %v", got, err)
	}
	if _, err := parseOAuthAccessToken(token, "test-secret", "https://other.example.com/mcp"); err == nil {
		t.Fatal("OAuth token for another resource was accepted")
	}
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

	p := &Provider{
		config:                Config{ClientID: "browser-client", ClientSecret: "client-secret"},
		introspectionEndpoint: server.URL,
	}
	subject, gotExpiry, err := p.introspectLoginAccessToken(context.Background(), "access-token")
	if err != nil {
		t.Fatalf("introspect login token: %v", err)
	}
	if subject != "subject-1" || gotExpiry.Unix() != expires {
		t.Fatalf("introspected identity = (%q, %v), want subject and expiry", subject, gotExpiry)
	}

	audience = []string{"mcp-client"}
	if _, _, err := p.introspectLoginAccessToken(context.Background(), "access-token"); err == nil || !strings.Contains(err.Error(), "unexpected audience") {
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
	p := &Provider{
		config: Config{
			ClientID:     "browser-client",
			ClientSecret: "client-secret",
			RedirectURL:  "https://stash.example.com/auth/callback",
		},
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
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/auth/callback?state=internal-state&code=auth-code", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "internal-state"})
	req.AddCookie(&http.Cookie{Name: nonceCookieName, Value: "nonce"})
	rec := httptest.NewRecorder()
	subject, gotExpiry, authErr := p.completeOIDCLogin(req, "internal-state", rec)
	if authErr != nil {
		t.Fatalf("complete login error = %v", authErr)
	}
	if subject != "subject-1" || gotExpiry.Unix() != expires {
		t.Fatalf("completed identity = (%q, %v), want subject and expiry", subject, gotExpiry)
	}
}

func TestOAuthCallbackWaitsForStashConsent(t *testing.T) {
	expires := time.Now().Add(time.Hour).Unix()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	defer upstream.Close()

	verifier := newHMACVerifier("https://auth.example.com/", "browser-client", "wrong-secret", false)
	p := &Provider{
		config: Config{
			Mode:           "oauth",
			ClientID:       "browser-client",
			ClientSecret:   "client-secret",
			RedirectURL:    "https://stash.example.com/auth/callback",
			APISecret:      testSigningSecret,
			MCPClientID:    "codex",
			MCPResourceURL: "https://stash.example.com/mcp",
		},
		oauth2Config: oauth2.Config{
			ClientID:     "browser-client",
			ClientSecret: "client-secret",
			RedirectURL:  "https://stash.example.com/auth/callback",
			Endpoint:     oauth2.Endpoint{TokenURL: upstream.URL + "/token"},
		},
		verifier:              verifier,
		hmacVerifier:          verifier,
		introspectionEndpoint: upstream.URL + "/introspect",
		clients: map[string]oauthClient{
			"codex": {ID: "codex", Name: "Codex", TokenEndpointAuthMethod: "none"},
		},
		pending: map[string]authorizationRequest{
			"internal-state": {
				ClientID:        "codex",
				RedirectURI:     "http://127.0.0.1:43123/callback",
				State:           "client-state",
				CodeChallenge:   base64.RawURLEncoding.EncodeToString(sha256.New().Sum(nil)),
				ChallengeMethod: "S256",
				Resource:        "https://stash.example.com/mcp",
				Scope:           oidc.ScopeOpenID,
				ExpiresAt:       time.Now().Add(time.Minute),
			},
		},
		codes:         make(map[string]authorizationCode),
		refreshTokens: make(map[string]refreshToken),
	}
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/auth/callback?state=internal-state&code=auth-code", nil)
	req.AddCookie(&http.Cookie{Name: stateCookieName, Value: "internal-state"})
	req.AddCookie(&http.Cookie{Name: nonceCookieName, Value: "nonce"})
	rec := httptest.NewRecorder()
	p.HandleCallback(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "연결 허용") {
		t.Fatalf("callback status=%d body=%s", rec.Code, rec.Body.String())
	}
	pending := p.pending["internal-state"]
	if pending.Subject != "subject-1" || pending.ConsentToken == "" || len(p.codes) != 0 {
		t.Fatalf("callback state subject=%q consent=%t codes=%d", pending.Subject, pending.ConsentToken != "", len(p.codes))
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == sessionCookieName && cookie.Value != "" {
			t.Fatal("callback issued a session before Stash consent")
		}
	}
}

func TestOAuthRedirectReplacesConflictingResponseParameters(t *testing.T) {
	p := newLocalOAuthProvider()
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/authorize", nil)
	rec := httptest.NewRecorder()
	p.oauthRedirect(rec, req, "http://127.0.0.1:43123/callback?tenant=one&code=old&state=old&iss=https%3A%2F%2Fattacker.example", url.Values{
		"code":  {"new-code"},
		"state": {"new-state"},
	})
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	query := location.Query()
	for key, want := range map[string]string{
		"code":   "new-code",
		"state":  "new-state",
		"iss":    "https://stash.example.com",
		"tenant": "one",
	} {
		if got := query[key]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %#v, want one value %q", key, got, want)
		}
	}
}

func TestOAuthAuthorizationServerMetadata(t *testing.T) {
	p := newLocalOAuthProvider()
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/.well-known/oauth-authorization-server", nil)
	rec := httptest.NewRecorder()
	p.HandleAuthorizationServerMetadata(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Issuer                   string   `json:"issuer"`
		Authorization            string   `json:"authorization_endpoint"`
		Token                    string   `json:"token_endpoint"`
		Registration             string   `json:"registration_endpoint"`
		ScopesSupported          []string `json:"scopes_supported"`
		PKCEMethods              []string `json:"code_challenge_methods_supported"`
		GrantTypes               []string `json:"grant_types_supported"`
		AuthorizationResponseISS bool     `json:"authorization_response_iss_parameter_supported"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if body.Issuer != "https://stash.example.com" || body.Authorization != "https://stash.example.com/authorize" || body.Token != "https://stash.example.com/oauth/token" || body.Registration != "https://stash.example.com/oauth/register" {
		t.Fatalf("metadata endpoints = %#v", body)
	}
	if len(body.PKCEMethods) != 1 || body.PKCEMethods[0] != "S256" || len(body.GrantTypes) != 2 {
		t.Fatalf("metadata OAuth capabilities = %#v", body)
	}
	if len(body.ScopesSupported) != 1 || body.ScopesSupported[0] != oidc.ScopeOpenID {
		t.Fatalf("metadata scopes = %#v, want only %q", body.ScopesSupported, oidc.ScopeOpenID)
	}
	if !body.AuthorizationResponseISS {
		t.Fatal("metadata did not advertise the authorization response issuer")
	}
}

func TestOAuthAuthorizeStartsPKCEBrokerFlow(t *testing.T) {
	p := newLocalOAuthProvider()
	p.oauth2Config.Endpoint.AuthURL = "https://auth.example.com/application/o/authorize/"
	verifier := testPKCEVerifier
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {"codex"},
		"redirect_uri":          {"http://127.0.0.1:43123/callback"},
		"state":                 {"client-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {"https://stash.example.com/mcp"},
		"scope":                 {"openid"},
	}
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/authorize?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	p.HandleAuthorize(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, body = %s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, p.oauth2Config.Endpoint.AuthURL) || !strings.Contains(location, "client_id=authentik-stash") || !strings.Contains(location, "state=") {
		t.Fatalf("upstream authorization location = %q", location)
	}
	if len(p.pending) != 1 {
		t.Fatalf("pending authorization requests = %d, want 1", len(p.pending))
	}
}

func TestOAuthAuthorizeCapsPendingRequests(t *testing.T) {
	p := newLocalOAuthProvider()
	p.oauth2Config.Endpoint.AuthURL = "https://auth.example.com/application/o/authorize/"
	for index := 0; index < maxPendingRequests; index++ {
		p.pending["pending-"+strconv.Itoa(index)] = authorizationRequest{ExpiresAt: time.Now().Add(time.Minute)}
	}
	sum := sha256.Sum256([]byte(testPKCEVerifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {"codex"},
		"redirect_uri":          {"http://127.0.0.1:43123/callback"},
		"state":                 {"client-state"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"resource":              {"https://stash.example.com/mcp"},
	}
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/authorize?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	p.HandleAuthorize(rec, req)
	if rec.Code != http.StatusServiceUnavailable || len(p.pending) != maxPendingRequests {
		t.Fatalf("pending cap status=%d pending=%d body=%s", rec.Code, len(p.pending), rec.Body.String())
	}
}

func TestPKCEVerifierValidation(t *testing.T) {
	for _, valid := range []string{
		testPKCEVerifier,
		strings.Repeat("a", 128),
		strings.Repeat("a", 42) + "~",
	} {
		if !validPKCEVerifier(valid) {
			t.Fatalf("valid verifier was rejected: %q", valid)
		}
	}
	for _, invalid := range []string{
		strings.Repeat("a", 42),
		strings.Repeat("a", 129),
		strings.Repeat("a", 42) + "/",
		strings.Repeat("a", 42) + " ",
	} {
		if validPKCEVerifier(invalid) {
			t.Fatalf("invalid verifier was accepted: %q", invalid)
		}
	}
}

func TestOAuthRequestValueValidation(t *testing.T) {
	validChallenge := base64.RawURLEncoding.EncodeToString(make([]byte, sha256.Size))
	if !validPKCEChallenge(validChallenge) {
		t.Fatal("expected a SHA-256 PKCE challenge to be accepted")
	}
	for _, challenge := range []string{
		validChallenge[:len(validChallenge)-1],
		validChallenge + "A",
		strings.Repeat("*", 43),
	} {
		if validPKCEChallenge(challenge) {
			t.Fatalf("expected invalid PKCE challenge %q to be rejected", challenge)
		}
	}

	for _, redirectURI := range []string{
		"https://user@example.com/callback",
		"http://user@127.0.0.1/callback",
	} {
		if validRedirectURI(redirectURI) {
			t.Fatalf("expected redirect URI with user info %q to be rejected", redirectURI)
		}
	}
}

func TestOAuthConsentIsRequiredBeforeAuthorizationCode(t *testing.T) {
	p := newLocalOAuthProvider()
	verifier := testPKCEVerifier
	sum := sha256.Sum256([]byte(verifier))
	p.pending["internal-state"] = authorizationRequest{
		ClientID:         "codex",
		RedirectURI:      "http://127.0.0.1:43123/callback",
		State:            "client-state",
		CodeChallenge:    base64.RawURLEncoding.EncodeToString(sum[:]),
		ChallengeMethod:  "S256",
		Resource:         "https://stash.example.com/mcp",
		Scope:            oidc.ScopeOpenID,
		ExpiresAt:        time.Now().Add(time.Minute),
		Subject:          "subject-1",
		SessionExpiresAt: time.Now().Add(time.Hour),
		ConsentToken:     "one-time-consent-token",
	}
	form := url.Values{
		"state":         {"internal-state"},
		"consent_token": {"one-time-consent-token"},
		"decision":      {"allow"},
	}
	req := httptest.NewRequest(http.MethodPost, "https://stash.example.com/oauth/consent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://stash.example.com")
	rec := httptest.NewRecorder()
	p.HandleConsent(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("consent status=%d body=%q", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse consent redirect: %v", err)
	}
	if location.Query().Get("code") == "" || location.Query().Get("state") != "client-state" || location.Query().Get("iss") != "https://stash.example.com" {
		t.Fatalf("consent redirect = %q", location.String())
	}
	if len(p.pending) != 0 || len(p.codes) != 1 {
		t.Fatalf("OAuth state after consent: pending=%d codes=%d", len(p.pending), len(p.codes))
	}
	if len(rec.Result().Cookies()) != 1 || rec.Result().Cookies()[0].Name != sessionCookieName {
		t.Fatalf("consent session cookies = %#v", rec.Result().Cookies())
	}

	replay := httptest.NewRecorder()
	p.HandleConsent(replay, req.Clone(req.Context()))
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replayed consent status=%d, want %d", replay.Code, http.StatusBadRequest)
	}
}

func TestOAuthConsentDenialAndCrossOriginRequestDoNotIssueCode(t *testing.T) {
	newPending := func() authorizationRequest {
		return authorizationRequest{
			ClientID:         "codex",
			RedirectURI:      "http://127.0.0.1:43123/callback",
			State:            "client-state",
			Resource:         "https://stash.example.com/mcp",
			Scope:            oidc.ScopeOpenID,
			ExpiresAt:        time.Now().Add(time.Minute),
			Subject:          "subject-1",
			SessionExpiresAt: time.Now().Add(time.Hour),
			ConsentToken:     "one-time-consent-token",
		}
	}
	form := url.Values{"state": {"internal-state"}, "consent_token": {"one-time-consent-token"}, "decision": {"deny"}}

	p := newLocalOAuthProvider()
	p.pending["internal-state"] = newPending()
	crossOrigin := httptest.NewRequest(http.MethodPost, "https://stash.example.com/oauth/consent", strings.NewReader(form.Encode()))
	crossOrigin.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOriginResponse := httptest.NewRecorder()
	p.HandleConsent(crossOriginResponse, crossOrigin)
	if crossOriginResponse.Code != http.StatusForbidden || len(p.pending) != 1 || len(p.codes) != 0 {
		t.Fatalf("cross-origin consent status=%d pending=%d codes=%d", crossOriginResponse.Code, len(p.pending), len(p.codes))
	}

	denial := httptest.NewRequest(http.MethodPost, "https://stash.example.com/oauth/consent", strings.NewReader(form.Encode()))
	denial.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	denial.Header.Set("Origin", "https://stash.example.com")
	denialResponse := httptest.NewRecorder()
	p.HandleConsent(denialResponse, denial)
	location, err := url.Parse(denialResponse.Header().Get("Location"))
	if err != nil || denialResponse.Code != http.StatusFound || location.Query().Get("error") != "access_denied" || location.Query().Get("iss") != "https://stash.example.com" {
		t.Fatalf("denial status=%d location=%q error=%v", denialResponse.Code, denialResponse.Header().Get("Location"), err)
	}
	if len(p.codes) != 0 || len(denialResponse.Result().Cookies()) != 0 {
		t.Fatalf("denial issued credentials: codes=%d cookies=%#v", len(p.codes), denialResponse.Result().Cookies())
	}
}

func TestOAuthAuthorizeWithoutScopeUsesOnlyOpenID(t *testing.T) {
	p := newLocalOAuthProvider()
	p.oauth2Config.Endpoint.AuthURL = "https://auth.example.com/application/o/authorize/"
	verifier := testPKCEVerifier
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {"codex"},
		"redirect_uri":          {"http://127.0.0.1:43123/callback"},
		"state":                 {"client-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {"https://stash.example.com/mcp"},
	}
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/authorize?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	p.HandleAuthorize(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, body = %s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse upstream authorization location: %v", err)
	}
	if got := location.Query().Get("scope"); got != oidc.ScopeOpenID {
		t.Fatalf("upstream scope = %q, want %q", got, oidc.ScopeOpenID)
	}
}

func TestOAuthAuthorizeDropsUnsupportedCachedScopes(t *testing.T) {
	p := newLocalOAuthProvider()
	p.oauth2Config.Endpoint.AuthURL = "https://auth.example.com/application/o/authorize/"
	verifier := testPKCEVerifier
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {"codex"},
		"redirect_uri":          {"http://127.0.0.1:43123/callback"},
		"state":                 {"client-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {"https://stash.example.com/mcp"},
		"scope":                 {"openid profile email"},
	}
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/authorize?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	p.HandleAuthorize(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, body = %s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse upstream authorization location: %v", err)
	}
	if got := location.Query().Get("scope"); got != oidc.ScopeOpenID {
		t.Fatalf("upstream scope = %q, want %q", got, oidc.ScopeOpenID)
	}
}

func TestOAuthAuthorizeRequiresResourceIndicator(t *testing.T) {
	p := newLocalOAuthProvider()
	p.oauth2Config.Endpoint.AuthURL = "https://auth.example.com/authorize"
	verifier := testPKCEVerifier
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {"codex"},
		"redirect_uri":          {"http://127.0.0.1:43123/callback"},
		"state":                 {"client-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/authorize?"+query.Encode(), nil)
	rec := httptest.NewRecorder()
	p.HandleAuthorize(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("authorize status = %d, want redirect with OAuth error", rec.Code)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse error redirect: %v", err)
	}
	if location.Host != "127.0.0.1:43123" || location.Query().Get("error") != "invalid_request" {
		t.Fatalf("resource error redirect = %q", location.String())
	}
}

func TestOAuthDynamicClientRegistrationAndPKCETokenExchange(t *testing.T) {
	p := newLocalOAuthProvider()
	registration := `{"client_name":"Codex","redirect_uris":["http://127.0.0.1:43123/callback"],"token_endpoint_auth_method":"none"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(registration))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.HandleOAuthRegister(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registration status = %d, want %d", rec.Code, http.StatusCreated)
	}
	var registered struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&registered); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if registered.ClientID == "" || !strings.HasPrefix(registered.ClientID, "stash_") {
		t.Fatalf("client_id = %q", registered.ClientID)
	}

	verifier := testPKCEVerifier
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	code := "one-time-code"
	p.codes[code] = authorizationCode{
		Subject:         "subject-1",
		ClientID:        registered.ClientID,
		RedirectURI:     "http://127.0.0.1:43123/callback",
		CodeChallenge:   challenge,
		ChallengeMethod: "S256",
		Resource:        "https://stash.example.com/mcp",
		Scope:           "openid",
		ExpiresAt:       time.Now().Add(time.Minute),
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {registered.ClientID},
		"code":          {code},
		"redirect_uri":  {"http://127.0.0.1:43123/callback"},
		"code_verifier": {verifier},
		"resource":      {"https://stash.example.com/mcp"},
	}
	tokenReq := httptest.NewRequest(http.MethodPost, "https://stash.example.com/oauth/token", strings.NewReader(form.Encode()))
	tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tokenRec := httptest.NewRecorder()
	p.HandleOAuthToken(tokenRec, tokenReq)
	if tokenRec.Code != http.StatusOK {
		t.Fatalf("token status = %d, body = %s", tokenRec.Code, tokenRec.Body.String())
	}
	var tokenResponse struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(tokenRec.Body).Decode(&tokenResponse); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if tokenResponse.AccessToken == "" || tokenResponse.RefreshToken == "" {
		t.Fatalf("missing OAuth tokens: %#v", tokenResponse)
	}
	verifyReq := httptest.NewRequest(http.MethodPost, "https://stash.example.com/mcp", nil)
	verifyReq.Header.Set("Authorization", "Bearer "+tokenResponse.AccessToken)
	if got, err := p.VerifyRequest(verifyReq); err != nil || got != "subject-1" {
		t.Fatalf("VerifyRequest = %q, %v", got, err)
	}
	if got, err := p.VerifyMCPRequest(verifyReq); err != nil || got != "subject-1" {
		t.Fatalf("VerifyMCPRequest = %q, %v", got, err)
	}

	// Authorization codes are single-use.
	replayReq := httptest.NewRequest(http.MethodPost, "https://stash.example.com/oauth/token", strings.NewReader(form.Encode()))
	replayReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replayRec := httptest.NewRecorder()
	p.HandleOAuthToken(replayRec, replayReq)
	if replayRec.Code != http.StatusBadRequest {
		t.Fatalf("replayed code status = %d, want %d", replayRec.Code, http.StatusBadRequest)
	}

	refreshForm := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {registered.ClientID},
		"refresh_token": {tokenResponse.RefreshToken},
		"resource":      {"https://stash.example.com/mcp"},
	}
	refreshReq := httptest.NewRequest(http.MethodPost, "https://stash.example.com/oauth/token", strings.NewReader(refreshForm.Encode()))
	refreshReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	refreshRec := httptest.NewRecorder()
	p.HandleOAuthToken(refreshRec, refreshReq)
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", refreshRec.Code, refreshRec.Body.String())
	}
	oldRefreshReq := httptest.NewRequest(http.MethodPost, "https://stash.example.com/oauth/token", strings.NewReader(refreshForm.Encode()))
	oldRefreshReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	oldRefreshRec := httptest.NewRecorder()
	p.HandleOAuthToken(oldRefreshRec, oldRefreshReq)
	if oldRefreshRec.Code != http.StatusBadRequest {
		t.Fatalf("rotated refresh status = %d, want %d", oldRefreshRec.Code, http.StatusBadRequest)
	}
}

func TestOAuthRegistrationRejectsTrailingJSON(t *testing.T) {
	p := newLocalOAuthProvider()
	body := `{"client_name":"Codex","redirect_uris":["http://127.0.0.1:43123/callback"]}{"extra":true}`
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.HandleOAuthRegister(rec, req)
	if rec.Code != http.StatusBadRequest || len(p.clients) != 1 {
		t.Fatalf("trailing registration status=%d clients=%d body=%s", rec.Code, len(p.clients), rec.Body.String())
	}
}

func TestOAuthRegistrationAllowsMissingClientName(t *testing.T) {
	p := newLocalOAuthProvider()
	body := `{"redirect_uris":["http://127.0.0.1:43123/callback"],"token_endpoint_auth_method":"none"}`
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.HandleOAuthRegister(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registration without client_name status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOAuthInMemoryStateIsBounded(t *testing.T) {
	p := newLocalOAuthProvider()
	now := time.Now()
	oldestID := ""
	for index := 0; index < maxOAuthClients-1; index++ {
		clientID := "dynamic-" + strconv.Itoa(index)
		p.clients[clientID] = oauthClient{
			ID:                      clientID,
			RedirectURIs:            []string{"https://client.example/callback"},
			TokenEndpointAuthMethod: "none",
			Dynamic:                 true,
			LastUsed:                now.Add(-time.Duration(index+1) * time.Minute),
		}
		oldestID = clientID
	}
	registration := `{"client_name":"New client","redirect_uris":["https://new.example/callback"]}`
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(registration))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	p.HandleOAuthRegister(rec, req)
	if rec.Code != http.StatusCreated || len(p.clients) != maxOAuthClients {
		t.Fatalf("bounded registration status=%d clients=%d", rec.Code, len(p.clients))
	}
	if _, exists := p.clients[oldestID]; exists {
		t.Fatalf("oldest unused dynamic client %q was not evicted", oldestID)
	}

	for index := 0; index < maxRefreshPerClient+3; index++ {
		p.storeRefreshTokenLocked("refresh-"+strconv.Itoa(index), refreshToken{
			Subject:   "subject-1",
			ClientID:  "codex",
			Resource:  "https://stash.example.com/mcp",
			CreatedAt: now.Add(time.Duration(index) * time.Second),
			ExpiresAt: now.Add(time.Hour),
		})
	}
	if count := p.refreshTokenCountLocked("subject-1", "codex", "https://stash.example.com/mcp"); count != maxRefreshPerClient {
		t.Fatalf("refresh tokens per client=%d, want %d", count, maxRefreshPerClient)
	}
	if _, exists := p.refreshTokens["refresh-0"]; exists {
		t.Fatal("oldest refresh token was not evicted")
	}
}

func TestOAuthRateLimitResetsAfterWindow(t *testing.T) {
	p := newLocalOAuthProvider()
	for index := 0; index < maxRegisterRequests; index++ {
		if !p.allowOAuthRequest(&p.registerRate, maxRegisterRequests) {
			t.Fatalf("request %d was limited too early", index+1)
		}
	}
	if p.allowOAuthRequest(&p.registerRate, maxRegisterRequests) {
		t.Fatal("registration rate limit allowed one request too many")
	}
	p.registerRate.StartedAt = time.Now().Add(-oauthRateWindow)
	if !p.allowOAuthRequest(&p.registerRate, maxRegisterRequests) {
		t.Fatal("registration rate limit did not reset")
	}
}

func TestOAuthClientAuthenticationUsesRegisteredMethod(t *testing.T) {
	client := oauthClient{TokenEndpointAuthMethod: "client_secret_basic", Secret: "secret"}
	if !oauthClientAuthenticated(client, "secret", true) {
		t.Fatal("client_secret_basic rejected valid Basic credentials")
	}
	if oauthClientAuthenticated(client, "secret", false) {
		t.Fatal("client_secret_basic accepted form credentials")
	}
	client.TokenEndpointAuthMethod = "client_secret_post"
	if !oauthClientAuthenticated(client, "secret", false) {
		t.Fatal("client_secret_post rejected valid form credentials")
	}
	if oauthClientAuthenticated(client, "secret", true) {
		t.Fatal("client_secret_post accepted Basic credentials")
	}
	public := oauthClient{TokenEndpointAuthMethod: "none"}
	if !oauthClientAuthenticated(public, "", false) {
		t.Fatal("public client rejected missing credentials")
	}
	if oauthClientAuthenticated(public, "", true) {
		t.Fatal("public client accepted Basic authentication")
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
	token, err := GenerateAPIToken("agent-1", testSigningSecret, time.Hour)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if got, err := p.VerifyMCPRequest(req); err != nil || got != "agent-1" {
		t.Fatalf("token mode request = %q, %v", got, err)
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
	if _, err := p.VerifyRequest(req); err == nil || !strings.Contains(err.Error(), "unsupported session credential") {
		t.Fatalf("ID-token-shaped cookie was accepted or returned the wrong error: %v", err)
	}
}

func TestAccessTokenAudienceAllowsConfiguredClientOrResource(t *testing.T) {
	p := &Provider{config: Config{
		MCPClientID:    "stash-codex",
		MCPResourceURL: "https://stash.example.com/mcp/",
	}}
	for _, audience := range [][]string{
		{"stash-codex"},
		{"https://stash.example.com/mcp"},
		{"other", "stash-codex"},
	} {
		if !p.accessTokenAudienceAllowed(audience) {
			t.Fatalf("audience %v was rejected", audience)
		}
	}
	if p.accessTokenAudienceAllowed([]string{"other"}) {
		t.Fatal("token for another application was accepted")
	}
}

func TestProtectedResourceMetadataUsesEndpointPath(t *testing.T) {
	p := &Provider{config: Config{Issuer: "https://auth.example.com/application/o/stash-codex/"}}
	req := httptest.NewRequest(http.MethodGet, "http://stash.example.com/.well-known/oauth-protected-resource/mcp", nil)
	rec := httptest.NewRecorder()
	p.HandleProtectedResourceMetadata(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if body.Resource != "http://stash.example.com/mcp" {
		t.Fatalf("resource = %q, want endpoint URL", body.Resource)
	}
	if len(body.AuthorizationServers) != 1 || body.AuthorizationServers[0] != "https://auth.example.com/application/o/stash-codex/" {
		t.Fatalf("authorization servers = %#v", body.AuthorizationServers)
	}
	if len(body.ScopesSupported) != 1 || body.ScopesSupported[0] != oidc.ScopeOpenID {
		t.Fatalf("scopes = %#v, want only %q", body.ScopesSupported, oidc.ScopeOpenID)
	}
}

func TestProtectedResourceMetadataHonorsConfiguredResourceURL(t *testing.T) {
	p := &Provider{config: Config{
		Issuer:         "https://auth.example.com/application/o/stash-codex/",
		MCPResourceURL: "https://stash.example.com/mcp/",
	}}
	req := httptest.NewRequest(http.MethodGet, "http://internal:8080/.well-known/oauth-protected-resource/mcp", nil)
	rec := httptest.NewRecorder()
	p.HandleProtectedResourceMetadata(rec, req)
	var body struct {
		Resource string `json:"resource"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if body.Resource != "https://stash.example.com/mcp" {
		t.Fatalf("resource = %q, want configured URL", body.Resource)
	}
}

func TestMCPUnauthorizedAdvertisesProtectedResourceMetadata(t *testing.T) {
	p := &Provider{config: Config{MCPResourceURL: "https://public.example.com/mcp"}}
	req := httptest.NewRequest(http.MethodPost, "http://internal:8080/mcp", nil)
	rec := httptest.NewRecorder()
	p.MCPUnauthorized(rec, req)

	if got, want := rec.Header().Get("WWW-Authenticate"), `Bearer resource_metadata="https://public.example.com/.well-known/oauth-protected-resource/mcp"`; got != want {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}
