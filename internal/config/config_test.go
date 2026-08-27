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
	if cfg.EmbeddingRetryInterval != time.Minute || cfg.EmbeddingRetryMaxInterval != time.Hour || cfg.EmbeddingRetryBatchSize != 100 {
		t.Fatalf("unexpected embedding retry defaults: interval=%s max=%s batch=%d", cfg.EmbeddingRetryInterval, cfg.EmbeddingRetryMaxInterval, cfg.EmbeddingRetryBatchSize)
	}
	if cfg.OpenAIRequestTimeout != 2*time.Minute || cfg.MCPToolTimeout != 2*time.Minute {
		t.Fatalf("unexpected request timeout defaults: openai=%s mcp=%s", cfg.OpenAIRequestTimeout, cfg.MCPToolTimeout)
	}
	if cfg.ReasonerContextTokens != 0 || cfg.ReasonerReservedTokens != 4096 || cfg.EmbeddingContextTokens != 0 {
		t.Fatalf("unexpected model context defaults: reasoner=%d reserve=%d embedding=%d", cfg.ReasonerContextTokens, cfg.ReasonerReservedTokens, cfg.EmbeddingContextTokens)
	}
	if cfg.MCPMaxResponseBytes != 32768 {
		t.Fatalf("MCPMaxResponseBytes = %d, want 32768", cfg.MCPMaxResponseBytes)
	}
}

func TestAuthenticationModeValidation(t *testing.T) {
	base := &Config{
		VectorDim: 1536, MaxResultSize: 10000, ContextTTL: time.Hour,
		EmbeddingRetryInterval: time.Minute, EmbeddingRetryMaxInterval: time.Hour, EmbeddingRetryBatchSize: 100,
		OpenAIRequestTimeout: 2 * time.Minute, MCPToolTimeout: 2 * time.Minute,
		HTTPAddr: ":8080", MCPMaxResponseBytes: 32768, ConsolidationBatchSize: 1,
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

func TestEmbeddingRetrySettingsValidation(t *testing.T) {
	base := Config{
		VectorDim: 1536, MaxResultSize: 10000, ContextTTL: time.Hour,
		EmbeddingRetryInterval: time.Minute, EmbeddingRetryMaxInterval: time.Hour, EmbeddingRetryBatchSize: 100,
		OpenAIRequestTimeout: 2 * time.Minute, MCPToolTimeout: 2 * time.Minute,
		HTTPAddr: ":8080", MCPMaxResponseBytes: 32768, ConsolidationBatchSize: 1,
		ConsolidationSimilarityThreshold: .5, ConsolidationDedupThreshold: .5,
		DecayFactor: .5, ExpiryThreshold: .1,
		HypothesisAutoConfirmThreshold: .9, HypothesisAutoRejectThreshold: .9,
	}

	invalidInterval := base
	invalidInterval.EmbeddingRetryInterval = 0
	if err := invalidInterval.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_EMBEDDING_RETRY_INTERVAL") {
		t.Fatalf("invalid retry interval error = %v", err)
	}

	invalidMaximum := base
	invalidMaximum.EmbeddingRetryMaxInterval = 30 * time.Second
	if err := invalidMaximum.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_EMBEDDING_RETRY_MAX_INTERVAL") {
		t.Fatalf("invalid retry maximum error = %v", err)
	}

	invalidBatch := base
	invalidBatch.EmbeddingRetryBatchSize = 0
	if err := invalidBatch.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_EMBEDDING_RETRY_BATCH_SIZE") {
		t.Fatalf("invalid retry batch error = %v", err)
	}

	invalidResponseLimit := base
	invalidResponseLimit.MCPMaxResponseBytes = 1000
	if err := invalidResponseLimit.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_MCP_MAX_RESPONSE_BYTES") {
		t.Fatalf("invalid MCP response limit error = %v", err)
	}

	invalidReasonerContext := base
	invalidReasonerContext.ReasonerContextTokens = -1
	if err := invalidReasonerContext.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_REASONER_CONTEXT_TOKENS") {
		t.Fatalf("invalid reasoner context error = %v", err)
	}
	invalidReserve := base
	invalidReserve.ReasonerReservedTokens = -1
	if err := invalidReserve.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_REASONER_RESERVED_TOKENS") {
		t.Fatalf("invalid reasoner reserve error = %v", err)
	}
	tooLittleContext := base
	tooLittleContext.ReasonerContextTokens = 4096
	tooLittleContext.ReasonerReservedTokens = 4096
	if err := tooLittleContext.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_REASONER_CONTEXT_TOKENS") {
		t.Fatalf("too-small reasoner context error = %v", err)
	}
	invalidEmbeddingContext := base
	invalidEmbeddingContext.EmbeddingContextTokens = -1
	if err := invalidEmbeddingContext.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_EMBEDDING_CONTEXT_TOKENS") {
		t.Fatalf("invalid embedding context error = %v", err)
	}
	invalidProviderTimeout := base
	invalidProviderTimeout.OpenAIRequestTimeout = 0
	if err := invalidProviderTimeout.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_OPENAI_REQUEST_TIMEOUT") {
		t.Fatalf("invalid provider timeout error = %v", err)
	}
	invalidToolTimeout := base
	invalidToolTimeout.MCPToolTimeout = 0
	if err := invalidToolTimeout.Validate(); err == nil || !strings.Contains(err.Error(), "STASH_MCP_TOOL_TIMEOUT") {
		t.Fatalf("invalid tool timeout error = %v", err)
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
