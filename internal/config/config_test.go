package config

import (
	"path/filepath"
	"testing"
)

func TestAuthenticationSettingsAreOptionalWhenDisabled(t *testing.T) {
	for key, value := range map[string]string{
		"STASH_POSTGRES_DSN":       "postgres://localhost/stash",
		"STASH_VECTOR_DIM":         "1536",
		"STASH_MAX_RESULT_SIZE":    "10000",
		"STASH_OPENAI_API_KEY":     "test-key",
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
}
