package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
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
