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
	"golang.org/x/oauth2"
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

func TestOAuthAuthorizationServerMetadata(t *testing.T) {
	p := newLocalOAuthProvider()
	req := httptest.NewRequest(http.MethodGet, "https://stash.example.com/.well-known/oauth-authorization-server", nil)
	rec := httptest.NewRecorder()
	p.HandleAuthorizationServerMetadata(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Issuer        string   `json:"issuer"`
		Authorization string   `json:"authorization_endpoint"`
		Token         string   `json:"token_endpoint"`
		Registration  string   `json:"registration_endpoint"`
		PKCEMethods   []string `json:"code_challenge_methods_supported"`
		GrantTypes    []string `json:"grant_types_supported"`
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
}

func TestOAuthAuthorizeStartsPKCEBrokerFlow(t *testing.T) {
	p := newLocalOAuthProvider()
	p.oauth2Config.Endpoint.AuthURL = "https://auth.example.com/application/o/authorize/"
	verifier := "pkce-verifier"
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

func TestOAuthAuthorizeRequiresResourceIndicator(t *testing.T) {
	p := newLocalOAuthProvider()
	p.oauth2Config.Endpoint.AuthURL = "https://auth.example.com/authorize"
	verifier := "pkce-verifier"
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

	verifier := "pkce-verifier"
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
	if len(body.ScopesSupported) == 0 || body.ScopesSupported[0] != "openid" {
		t.Fatalf("scopes = %#v", body.ScopesSupported)
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

func TestMCPUnauthorizedAdvertisesPathMetadata(t *testing.T) {
	p := &Provider{}
	req := httptest.NewRequest(http.MethodPost, "https://stash.example.com/mcp", nil)
	rec := httptest.NewRecorder()
	p.MCPUnauthorized(rec, req)
	if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer resource_metadata="https://stash.example.com/.well-known/oauth-protected-resource/mcp"` {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestMCPUnauthorizedUsesConfiguredResourceHost(t *testing.T) {
	p := &Provider{config: Config{MCPResourceURL: "https://public.example.com/mcp"}}
	req := httptest.NewRequest(http.MethodPost, "http://internal:8080/mcp", nil)
	rec := httptest.NewRecorder()
	p.MCPUnauthorized(rec, req)

	if got, want := rec.Header().Get("WWW-Authenticate"), `Bearer resource_metadata="https://public.example.com/.well-known/oauth-protected-resource/mcp"`; got != want {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
	}
}
