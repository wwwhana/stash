package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAuthenticationSettingsAreOptionalWhenDisabled(t *testing.T) {
	for key, value := range map[string]string{
		"STASH_POSTGRES_DSN":       "postgres://localhost/stash",
		"STASH_VECTOR_DIM":         "1536",
		"STASH_MAX_RESULT_SIZE":    "10000",
		"STASH_OPENAI_BASE_URL":    "https://example.invalid/v1",
		"STASH_EMBEDDING_MODEL":    "embed",
		"STASH_REASONER_MODEL":     "reason",
		"STASH_CONTEXT_TTL":        "1h",
		"STASH_HTTP_ADDR":          ":8080",
		"STASH_LOG_LEVEL":          "info",
		"STASH_LOG_FORMAT":         "text",
		"STASH_AUTH_MODE":          "none",
		"STASH_AUTH_ISSUER":        "",
		"STASH_AUTH_CLIENT_ID":     "",
		"STASH_AUTH_CLIENT_SECRET": "",
		"STASH_AUTH_REDIRECT_URL":  "",
		"STASH_AUTH_API_SECRET":    "",
	} {
		t.Setenv(key, value)
	}

	cfg, err := NewFromFile(filepath.Join(t.TempDir(), "missing.env"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.AuthMode != "none" || cfg.AuthIssuer != "" || cfg.AuthAPISecret != "" {
		t.Fatalf("unexpected disabled auth config: %#v", cfg)
	}
	if cfg.AuthTokenTTL.Hours() != 720 {
		t.Fatalf("AuthTokenTTL = %s, want 720h", cfg.AuthTokenTTL)
	}
	if cfg.OpenAIAPIKey != "" {
		t.Fatalf("OpenAIAPIKey = %q, want empty", cfg.OpenAIAPIKey)
	}
}

func TestAuthenticationModeValidation(t *testing.T) {
	base := &Config{
		VectorDim: 1536, MaxResultSize: 10000, ContextTTL: time.Hour,
		HTTPAddr: ":8080", ConsolidationBatchSize: 1,
		ConsolidationSimilarityThreshold: .5, ConsolidationDedupThreshold: .5,
		DecayFactor: .5, ExpiryThreshold: .1,
		HypothesisAutoConfirmThreshold: .9, HypothesisAutoRejectThreshold: .9,
	}
	for _, mode := range []string{"none", "oauth", "oidc", "stdio"} {
		cfg := *base
		cfg.AuthMode = mode
		if err := cfg.Validate(); err != nil {
			t.Fatalf("mode %q rejected: %v", mode, err)
		}
	}
	invalid := *base
	invalid.AuthMode = "basic"
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_AUTH_MODE") {
		t.Fatalf("invalid auth mode error = %v", err)
	}
}

func TestOAuthPrefixedAuthenticationAliases(t *testing.T) {
	for key, value := range map[string]string{
		"STASH_POSTGRES_DSN":             "postgres://localhost/stash",
		"STASH_VECTOR_DIM":               "1536",
		"STASH_MAX_RESULT_SIZE":          "10000",
		"STASH_OPENAI_BASE_URL":          "https://example.invalid/v1",
		"STASH_EMBEDDING_MODEL":          "embed",
		"STASH_REASONER_MODEL":           "reason",
		"STASH_CONTEXT_TTL":              "1h",
		"STASH_HTTP_ADDR":                ":8080",
		"STASH_LOG_LEVEL":                "info",
		"STASH_LOG_FORMAT":               "text",
		"STASH_AUTH_MODE":                "oauth",
		"STASH_AUTH_OAUTH_ISSUER":        "https://auth.example.com/",
		"STASH_AUTH_OAUTH_CLIENT_ID":     "stash",
		"STASH_AUTH_OAUTH_CLIENT_SECRET": "secret",
		"STASH_AUTH_OAUTH_REDIRECT_URL":  "https://stash.example.com/auth/callback",
		"STASH_AUTH_OAUTH_API_SECRET":    "signing-secret",
		"STASH_AUTH_OAUTH_RESOURCE_URL":  "https://stash.example.com/mcp",
	} {
		t.Setenv(key, value)
	}
	cfg, err := NewFromFile(filepath.Join(t.TempDir(), "missing.env"))
	if err != nil {
		t.Fatalf("load OAuth alias config: %v", err)
	}
	if cfg.AuthIssuer != "https://auth.example.com/" || cfg.AuthClientID != "stash" || cfg.AuthClientSecret != "secret" || cfg.AuthRedirectURL != "https://stash.example.com/auth/callback" || cfg.AuthAPISecret != "signing-secret" || cfg.AuthMCPResourceURL != "https://stash.example.com/mcp" {
		t.Fatalf("OAuth aliases were not applied: %#v", cfg)
	}
}
