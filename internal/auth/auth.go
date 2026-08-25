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
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	stateCookieName    = "oauthstate"
	nonceCookieName    = "oidc_nonce"
	sessionCookieName  = "stash_session"
	apiTokenPrefix     = "stash_api_"
	sessionTokenPrefix = "stash_session_"
	defaultTokenTTL    = 30 * 24 * time.Hour
)

// Config contains the settings needed by the HTTP authentication boundary.
type Config struct {
	Mode           string
	Issuer         string
	ClientID       string
	MCPClientID    string
	ClientSecret   string
	RedirectURL    string
	APISecret      string
	MCPResourceURL string
	CookieSecure   bool
	APITokenTTL    time.Duration
}

type Provider struct {
	config         Config
	oidcProvider   *oidc.Provider
	oauth2Config   oauth2.Config
	verifier       *oidc.IDTokenVerifier
	accessVerifier *oidc.IDTokenVerifier
}

// Init returns nil when authentication is explicitly disabled.
func Init(ctx context.Context, cfg Config) (*Provider, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" || mode == "none" {
		log.Println("Auth mode: none (HTTP authentication is disabled)")
		return nil, nil
	}
	if mode != "oidc" {
		return nil, fmt.Errorf("unsupported auth mode: %q", cfg.Mode)
	}
	if cfg.Issuer == "" {
		return nil, errors.New("OIDC mode requires an issuer")
	}
	if cfg.MCPClientID == "" && strings.TrimSpace(cfg.MCPResourceURL) == "" {
		return nil, errors.New("OIDC mode requires STASH_AUTH_MCP_CLIENT_ID or STASH_AUTH_MCP_RESOURCE_URL")
	}
	// The browser login is optional: an MCP resource server can operate without
	// keeping a second confidential OAuth client. If any browser setting is
	// supplied, require the complete set so a half-configured /auth/login path
	// cannot fail later with a cryptic redirect or callback error.
	webLoginConfigured := cfg.ClientID != "" || cfg.ClientSecret != "" || cfg.RedirectURL != ""
	if webLoginConfigured && (cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "") {
		return nil, errors.New("browser OIDC login requires client ID, client secret, and redirect URL together")
	}
	if cfg.APISecret == "" {
		return nil, errors.New("OIDC mode requires STASH_AUTH_API_SECRET")
	}
	if cfg.APITokenTTL <= 0 {
		cfg.APITokenTTL = defaultTokenTTL
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("initialize OIDC provider: %w", err)
	}

	var oauth2Config oauth2.Config
	var verifier *oidc.IDTokenVerifier
	if webLoginConfigured {
		oauth2Config = oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		}
		verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	}
	// Codex sends an OAuth access token to the MCP resource. Its audience may
	// be a separate public OAuth client, so verify the issuer, signature, and
	// expiry here and apply the configured audience check below. The audience is
	// mandatory; accepting any valid token from the issuer would let a token
	// issued for another application call this MCP server.
	accessVerifier := provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	log.Printf("Auth mode: oidc (issuer: %s)", cfg.Issuer)
	return &Provider{
		config:         cfg,
		oidcProvider:   provider,
		oauth2Config:   oauth2Config,
		verifier:       verifier,
		accessVerifier: accessVerifier,
	}, nil
}

func (p *Provider) HandleLogin(w http.ResponseWriter, r *http.Request) {
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

	state, nonce, err := setOAuthCookies(w, p.config.CookieSecure)
	if err != nil {
		http.Error(w, "could not start login", http.StatusInternalServerError)
		return
	}

	url := p.oauth2Config.AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", nonce))
	http.Redirect(w, r, url, http.StatusFound)
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

	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" {
		http.Error(w, "login state is missing", http.StatusBadRequest)
		return
	}
	nonceCookie, err := r.Cookie(nonceCookieName)
	if err != nil || nonceCookie.Value == "" {
		http.Error(w, "login nonce is missing", http.StatusBadRequest)
		return
	}
	clearOAuthCookies(w, p.config.CookieSecure)

	providedState := r.FormValue("state")
	if len(providedState) != len(stateCookie.Value) || subtle.ConstantTimeCompare([]byte(providedState), []byte(stateCookie.Value)) != 1 {
		http.Error(w, "invalid login state", http.StatusBadRequest)
		return
	}
	code := r.FormValue("code")
	if code == "" {
		http.Error(w, "authorization code is missing", http.StatusBadRequest)
		return
	}

	token, err := p.oauth2Config.Exchange(r.Context(), code)
	if err != nil {
		http.Error(w, "could not complete login", http.StatusBadGateway)
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		http.Error(w, "identity token is missing", http.StatusBadGateway)
		return
	}

	idToken, err := p.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		http.Error(w, "identity token is invalid", http.StatusUnauthorized)
		return
	}
	if idToken.Nonce == "" || len(idToken.Nonce) != len(nonceCookie.Value) || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(nonceCookie.Value)) != 1 {
		http.Error(w, "invalid login nonce", http.StatusUnauthorized)
		return
	}

	var claims struct {
		Sub string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.Sub == "" {
		http.Error(w, "identity token has no stable subject", http.StatusUnauthorized)
		return
	}

	expiresAt := idToken.Expiry
	if expiresAt.IsZero() || !expiresAt.After(time.Now()) {
		http.Error(w, "identity token is expired", http.StatusUnauthorized)
		return
	}
	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	session, err := generateSessionToken(claims.Sub, p.config.APISecret, expiresAt)
	if err != nil {
		http.Error(w, "could not create login session", http.StatusInternalServerError)
		return
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
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (p *Provider) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
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
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (p *Provider) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	status := map[string]any{"auth_mode": "none", "authenticated": false}
	if p != nil {
		status["auth_mode"] = "oidc"
		if user, err := p.VerifyRequest(r); err == nil && user != "" {
			status["authenticated"] = true
			status["user"] = user
		}
	}
	_ = json.NewEncoder(w).Encode(status)
}

// HandleProtectedResourceMetadata serves RFC 9728 metadata for Codex and
// other MCP OAuth clients. Authentik remains the authorization server; Stash
// only describes the protected MCP resource here.
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
		"authorization_servers": []string{p.config.Issuer},
		"scopes_supported":      []string{"openid", "profile", "email"},
		"resource_name":         "Stash Memory MCP",
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// MCPUnauthorized writes the RFC 6750 challenge that points an OAuth client
// to the protected-resource metadata for the endpoint it requested.
func (p *Provider) MCPUnauthorized(w http.ResponseWriter, r *http.Request) {
	metadataURL := p.protectedResourceMetadataURL(r)
	if metadataURL == "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="stash"`)
	} else {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+metadataURL+`"`)
	}
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// VerifyRequest validates a signed Stash session/API token or an OIDC access
// token sent as a bearer credential. OIDC ID tokens are deliberately not
// accepted here: they are meant for the OAuth client, not this resource server.
// It returns the stable OIDC subject, never a mutable email claim.
func (p *Provider) VerifyRequest(r *http.Request) (string, error) {
	if p == nil {
		return "", errors.New("authentication is disabled")
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
		return parseSessionToken(rawToken, p.config.APISecret)
	}
	if strings.HasPrefix(rawToken, apiTokenPrefix) {
		return parseStashToken(rawToken, p.config.APISecret)
	}
	if !bearer {
		return "", errors.New("unsupported session credential")
	}
	return p.verifyOIDCAccessToken(r, rawToken)
}

func (p *Provider) verifyOIDCAccessToken(r *http.Request, rawToken string) (string, error) {
	if p.accessVerifier == nil {
		return "", errors.New("OIDC access-token verifier is unavailable")
	}
	accessToken, err := p.accessVerifier.Verify(r.Context(), rawToken)
	if err != nil {
		return "", fmt.Errorf("invalid OIDC access token: %w", err)
	}
	if !p.accessTokenAudienceAllowed(accessToken.Audience) {
		return "", fmt.Errorf("OIDC access token has unexpected audience %q", accessToken.Audience)
	}
	return subjectFromToken(accessToken)
}

func (p *Provider) accessTokenAudienceAllowed(audience []string) bool {
	expected := make([]string, 0, 2)
	if clientID := strings.TrimSpace(p.config.MCPClientID); clientID != "" {
		expected = append(expected, clientID)
	}
	if resourceURL := strings.TrimRight(strings.TrimSpace(p.config.MCPResourceURL), "/"); resourceURL != "" {
		expected = append(expected, resourceURL)
	}
	for _, actual := range audience {
		for _, wanted := range expected {
			if actual == wanted || strings.TrimRight(actual, "/") == wanted {
				return true
			}
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
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
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
	w.Header().Set("Content-Type", "application/json")
	if p == nil {
		http.Error(w, `{"error":"authentication is disabled"}`, http.StatusNotFound)
		return
	}
	user, err := p.VerifyRequest(r)
	if err != nil || user == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	token, err := generateStashToken(user, p.config.APISecret, p.config.APITokenTTL)
	if err != nil {
		http.Error(w, `{"error":"token generation is unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
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
	expires := time.Now().Add(10 * time.Minute)
	for name, value := range map[string]string{stateCookieName: state, nonceCookieName: nonce} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    value,
			Expires:  expires,
			MaxAge:   600,
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

func parseStashToken(token, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("API secret is not configured")
	}
	if !strings.HasPrefix(token, apiTokenPrefix) {
		return "", errors.New("invalid API token prefix")
	}
	parts := strings.Split(strings.TrimPrefix(token, apiTokenPrefix), ".")
	if len(parts) != 4 {
		return "", errors.New("invalid API token format")
	}
	payload := strings.Join(parts[:3], ".")
	expected, err := hex.DecodeString(sign(payload, secret))
	if err != nil {
		return "", errors.New("invalid API token signature")
	}
	provided, err := hex.DecodeString(parts[3])
	if err != nil || subtle.ConstantTimeCompare(provided, expected) != 1 {
		return "", errors.New("invalid API token signature")
	}

	decodedUser, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(decodedUser) == 0 {
		return "", errors.New("invalid API token user")
	}
	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expiresAt <= time.Now().Unix() {
		return "", errors.New("API token expired")
	}
	return string(decodedUser), nil
}

func sign(payload, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(payload))
	return hex.EncodeToString(h.Sum(nil))
}
