package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/secrets"
	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	// Store (PostgreSQL only)
	StoreDSN      string `env:"STASH_POSTGRES_DSN,required"`
	VectorDim     int    `env:"STASH_VECTOR_DIM" envDefault:"0"`
	MaxResultSize int    `env:"STASH_MAX_RESULT_SIZE,required"`

	// OpenAI-compatible environment provider. All of these are optional: a
	// deployment can register providers in the database instead, and a
	// server without any embedding provider still stores memories and
	// searches them with trigram matching.
	OpenAIAPIKey              string        `env:"STASH_OPENAI_API_KEY" envDefault:""`
	OpenAIBaseURL             string        `env:"STASH_OPENAI_BASE_URL" envDefault:""`
	EmbeddingModel            string        `env:"STASH_EMBEDDING_MODEL" envDefault:""`
	ReasonerModel             string        `env:"STASH_REASONER_MODEL" envDefault:""`
	EmbeddingRetryInterval    time.Duration `env:"STASH_EMBEDDING_RETRY_INTERVAL" envDefault:"1m"`
	EmbeddingRetryMaxInterval time.Duration `env:"STASH_EMBEDDING_RETRY_MAX_INTERVAL" envDefault:"1h"`
	EmbeddingRetryBatchSize   int           `env:"STASH_EMBEDDING_RETRY_BATCH_SIZE" envDefault:"100"`
	// OpenAI-compatible providers are allowed to be remote or local. Keep one
	// request from holding an MCP call (or the background retry worker) forever.
	OpenAIRequestTimeout time.Duration `env:"STASH_OPENAI_REQUEST_TIMEOUT" envDefault:"2m"`
	// Model input limits are provider/model-specific. MCP cannot discover them
	// or the caller's remaining context, so deployments may set these values to
	// make consolidation and long-memory embedding split before a request fails.
	ReasonerContextTokens  int `env:"STASH_REASONER_CONTEXT_TOKENS" envDefault:"0"`
	ReasonerReservedTokens int `env:"STASH_REASONER_RESERVED_TOKENS" envDefault:"4096"`
	EmbeddingContextTokens int `env:"STASH_EMBEDDING_CONTEXT_TOKENS" envDefault:"0"`

	// EmbeddingCache keeps computed vectors in embedding_cache so identical
	// text is not sent to the provider twice. It is an optimization only.
	EmbeddingCache bool `env:"STASH_EMBEDDING_CACHE" envDefault:"true"`

	// Secrets seal provider API keys stored in the database. The active key
	// seals new values; previous keys (comma-separated) only open old ones so
	// rotation never needs every credential re-entered at once.
	SecretsKey         string `env:"STASH_SECRETS_KEY" envDefault:""`
	SecretsKeyPrevious string `env:"STASH_SECRETS_KEY_PREVIOUS" envDefault:""`

	// Memory
	ContextTTL time.Duration `env:"STASH_CONTEXT_TTL,required"`

	// Server
	HTTPAddr            string `env:"STASH_HTTP_ADDR,required"`
	LogLevel            string `env:"STASH_LOG_LEVEL,required"`
	LogFormat           string `env:"STASH_LOG_FORMAT,required"`
	MCPMaxResponseBytes int    `env:"STASH_MCP_MAX_RESPONSE_BYTES" envDefault:"32768"`
	// Tool handlers use a separate bound so database-only calls cannot remain
	// queued behind a stalled provider request until the client's timeout.
	MCPToolTimeout time.Duration `env:"STASH_MCP_TOOL_TIMEOUT" envDefault:"2m"`

	// Authentication
	AuthMode            string        `env:"STASH_AUTH_MODE" envDefault:"none"`
	AuthIssuer          string        `env:"STASH_AUTH_ISSUER" envDefault:""`
	AuthClientID        string        `env:"STASH_AUTH_CLIENT_ID" envDefault:""`
	AuthMCPClientID     string        `env:"STASH_AUTH_MCP_CLIENT_ID" envDefault:""`
	AuthClientSecret    string        `env:"STASH_AUTH_CLIENT_SECRET" envDefault:""`
	AuthRedirectURL     string        `env:"STASH_AUTH_REDIRECT_URL" envDefault:""`
	AuthAPISecret       string        `env:"STASH_AUTH_API_SECRET" envDefault:""`
	AuthMCPResourceURL  string        `env:"STASH_AUTH_MCP_RESOURCE_URL" envDefault:""`
	AuthCookieSecure    bool          `env:"STASH_AUTH_COOKIE_SECURE" envDefault:"true"`
	AuthTokenTTL        time.Duration `env:"STASH_AUTH_TOKEN_TTL" envDefault:"720h"`
	AuthAccessTokenTTL  time.Duration `env:"STASH_AUTH_ACCESS_TOKEN_TTL" envDefault:"1h"`
	AuthRefreshTokenTTL time.Duration `env:"STASH_AUTH_REFRESH_TOKEN_TTL" envDefault:"720h"`
	AuthSessionTTL      time.Duration `env:"STASH_AUTH_SESSION_TTL" envDefault:"720h"`
	AuthStdioToken      string        `env:"STASH_AUTH_STDIO_TOKEN" envDefault:""`
	// Admin maintenance accepts either an authenticated OIDC subject listed
	// here or the separate static token below. Keep this independent from the
	// MCP API secret so a maintenance credential cannot sign user sessions.
	AdminSubjects string `env:"STASH_ADMIN_SUBJECTS" envDefault:""`
	AdminToken    string `env:"STASH_ADMIN_TOKEN" envDefault:""`

	// OAuth-prefixed aliases make the profile explicit while preserving the
	// original STASH_AUTH_* names used by existing deployments.
	AuthOAuthIssuer          string        `env:"STASH_AUTH_OAUTH_ISSUER" envDefault:""`
	AuthOAuthClientID        string        `env:"STASH_AUTH_OAUTH_CLIENT_ID" envDefault:""`
	AuthOAuthMCPClientID     string        `env:"STASH_AUTH_OAUTH_MCP_CLIENT_ID" envDefault:""`
	AuthOAuthClientSecret    string        `env:"STASH_AUTH_OAUTH_CLIENT_SECRET" envDefault:""`
	AuthOAuthRedirectURL     string        `env:"STASH_AUTH_OAUTH_REDIRECT_URL" envDefault:""`
	AuthOAuthAPISecret       string        `env:"STASH_AUTH_OAUTH_API_SECRET" envDefault:""`
	AuthOAuthResourceURL     string        `env:"STASH_AUTH_OAUTH_RESOURCE_URL" envDefault:""`
	AuthOAuthCookieSecureRaw string        `env:"STASH_AUTH_OAUTH_COOKIE_SECURE" envDefault:""`
	AuthOAuthTokenTTL        time.Duration `env:"STASH_AUTH_OAUTH_TOKEN_TTL" envDefault:"0s"`
	AuthOAuthAccessTokenTTL  time.Duration `env:"STASH_AUTH_OAUTH_ACCESS_TOKEN_TTL" envDefault:"0s"`
	AuthOAuthRefreshTokenTTL time.Duration `env:"STASH_AUTH_OAUTH_REFRESH_TOKEN_TTL" envDefault:"0s"`
	AuthOAuthStdioToken      string        `env:"STASH_AUTH_OAUTH_STDIO_TOKEN" envDefault:""`

	// Consolidation
	ConsolidationBatchSize           int     `env:"STASH_CONSOLIDATION_BATCH_SIZE" envDefault:"100"`
	ConsolidationSimilarityThreshold float64 `env:"STASH_CONSOLIDATION_SIMILARITY_THRESHOLD" envDefault:"0.85"`
	ConsolidationDedupThreshold      float64 `env:"STASH_CONSOLIDATION_DEDUP_THRESHOLD" envDefault:"0.85"`
	ConsolidationWindow              string  `env:"STASH_CONSOLIDATION_WINDOW" envDefault:"168h"`
	DecayFactor                      float64 `env:"STASH_DECAY_FACTOR" envDefault:"0.95"`
	ExpiryThreshold                  float32 `env:"STASH_EXPIRY_THRESHOLD" envDefault:"0.1"`
	HypothesisAutoConfirmThreshold   float32 `env:"STASH_HYPOTHESIS_AUTO_CONFIRM_THRESHOLD" envDefault:"0.9"`
	HypothesisAutoRejectThreshold    float32 `env:"STASH_HYPOTHESIS_AUTO_REJECT_THRESHOLD" envDefault:"0.9"`
}

func NewFromFile(filename string) (*Config, error) {
	if _, err := os.Stat(filename); err == nil {
		if err := godotenv.Load(filename); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	cfg := &Config{}
	opts := env.Options{
		RequiredIfNoDef: true,
	}
	if err := env.ParseWithOptions(cfg, opts); err != nil {
		return nil, err
	}
	cfg.applyAuthAliases()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyAuthAliases() {
	if c.AuthIssuer == "" {
		c.AuthIssuer = c.AuthOAuthIssuer
	}
	if c.AuthClientID == "" {
		c.AuthClientID = c.AuthOAuthClientID
	}
	if c.AuthMCPClientID == "" {
		c.AuthMCPClientID = c.AuthOAuthMCPClientID
	}
	if c.AuthClientSecret == "" {
		c.AuthClientSecret = c.AuthOAuthClientSecret
	}
	if c.AuthRedirectURL == "" {
		c.AuthRedirectURL = c.AuthOAuthRedirectURL
	}
	if c.AuthAPISecret == "" {
		c.AuthAPISecret = c.AuthOAuthAPISecret
	}
	if c.AuthMCPResourceURL == "" {
		c.AuthMCPResourceURL = c.AuthOAuthResourceURL
	}
	if c.AuthStdioToken == "" {
		c.AuthStdioToken = c.AuthOAuthStdioToken
	}
	if raw := strings.TrimSpace(c.AuthOAuthCookieSecureRaw); raw != "" {
		if parsed, err := strconv.ParseBool(raw); err == nil {
			c.AuthCookieSecure = parsed
		}
	}
	if c.AuthOAuthTokenTTL > 0 {
		c.AuthTokenTTL = c.AuthOAuthTokenTTL
	}
	if c.AuthOAuthAccessTokenTTL > 0 {
		c.AuthAccessTokenTTL = c.AuthOAuthAccessTokenTTL
	}
	if c.AuthOAuthRefreshTokenTTL > 0 {
		c.AuthRefreshTokenTTL = c.AuthOAuthRefreshTokenTTL
	}
}

// Validate rejects configurations that would otherwise fail much later during
// database startup or the first MCP call.
func (c *Config) Validate() error {
	switch strings.ToLower(strings.TrimSpace(c.AuthMode)) {
	case "", "none", "oauth", "oidc", "token", "stdio":
	default:
		return fmt.Errorf("STASH_AUTH_MODE must be one of none, oauth, oidc, token, or stdio")
	}
	if raw := strings.TrimSpace(c.AuthOAuthCookieSecureRaw); raw != "" {
		if _, err := strconv.ParseBool(raw); err != nil {
			return fmt.Errorf("STASH_AUTH_OAUTH_COOKIE_SECURE must be true or false")
		}
	}
	if c.VectorDim < 0 || c.VectorDim > 2000 {
		return fmt.Errorf("STASH_VECTOR_DIM must be between 0 and 2000")
	}
	if strings.TrimSpace(c.OpenAIBaseURL) == "" && (strings.TrimSpace(c.EmbeddingModel) != "" || strings.TrimSpace(c.ReasonerModel) != "") {
		return fmt.Errorf("STASH_OPENAI_BASE_URL is required when STASH_EMBEDDING_MODEL or STASH_REASONER_MODEL is set")
	}
	if strings.TrimSpace(c.EmbeddingModel) != "" && c.VectorDim <= 0 {
		return fmt.Errorf("STASH_VECTOR_DIM must be greater than zero when STASH_EMBEDDING_MODEL is set")
	}
	if strings.TrimSpace(c.SecretsKey) != "" {
		if _, err := secrets.ParseKey(c.SecretsKey); err != nil {
			return fmt.Errorf("STASH_SECRETS_KEY must be 64 hexadecimal characters (openssl rand -hex 32)")
		}
	}
	for _, previous := range strings.Split(c.SecretsKeyPrevious, ",") {
		if strings.TrimSpace(previous) == "" {
			continue
		}
		if _, err := secrets.ParseKey(previous); err != nil {
			return fmt.Errorf("STASH_SECRETS_KEY_PREVIOUS entries must be 64 hexadecimal characters")
		}
	}
	if c.MaxResultSize <= 0 {
		return fmt.Errorf("STASH_MAX_RESULT_SIZE must be greater than zero")
	}
	if c.ContextTTL <= 0 {
		return fmt.Errorf("STASH_CONTEXT_TTL must be greater than zero")
	}
	if c.EmbeddingRetryInterval <= 0 {
		return fmt.Errorf("STASH_EMBEDDING_RETRY_INTERVAL must be greater than zero")
	}
	if c.EmbeddingRetryMaxInterval < c.EmbeddingRetryInterval {
		return fmt.Errorf("STASH_EMBEDDING_RETRY_MAX_INTERVAL must be greater than or equal to STASH_EMBEDDING_RETRY_INTERVAL")
	}
	if c.EmbeddingRetryBatchSize <= 0 {
		return fmt.Errorf("STASH_EMBEDDING_RETRY_BATCH_SIZE must be greater than zero")
	}
	if c.OpenAIRequestTimeout <= 0 {
		return fmt.Errorf("STASH_OPENAI_REQUEST_TIMEOUT must be greater than zero")
	}
	if c.ReasonerContextTokens < 0 {
		return fmt.Errorf("STASH_REASONER_CONTEXT_TOKENS must not be negative")
	}
	if c.ReasonerReservedTokens < 0 {
		return fmt.Errorf("STASH_REASONER_RESERVED_TOKENS must not be negative")
	}
	if c.ReasonerContextTokens > 0 && c.ReasonerContextTokens <= c.ReasonerReservedTokens {
		return fmt.Errorf("STASH_REASONER_CONTEXT_TOKENS must be greater than STASH_REASONER_RESERVED_TOKENS")
	}
	if c.EmbeddingContextTokens < 0 {
		return fmt.Errorf("STASH_EMBEDDING_CONTEXT_TOKENS must not be negative")
	}
	if c.HTTPAddr == "" {
		return fmt.Errorf("STASH_HTTP_ADDR must not be empty")
	}
	if c.MCPMaxResponseBytes < 1024 {
		return fmt.Errorf("STASH_MCP_MAX_RESPONSE_BYTES must be at least 1024")
	}
	if c.MCPToolTimeout <= 0 {
		return fmt.Errorf("STASH_MCP_TOOL_TIMEOUT must be greater than zero")
	}
	if c.AuthTokenTTL < 0 {
		return fmt.Errorf("STASH_AUTH_TOKEN_TTL must not be negative")
	}
	if c.AuthAccessTokenTTL < 0 || c.AuthAccessTokenTTL > time.Hour {
		return fmt.Errorf("STASH_AUTH_ACCESS_TOKEN_TTL must not be negative or greater than 1h")
	}
	if c.AuthSessionTTL < 0 {
		return fmt.Errorf("STASH_AUTH_SESSION_TTL must not be negative")
	}
	if c.AuthRefreshTokenTTL < 0 {
		return fmt.Errorf("STASH_AUTH_REFRESH_TOKEN_TTL must not be negative")
	}
	if c.AuthRefreshTokenTTL > 0 && c.AuthAccessTokenTTL > 0 && c.AuthRefreshTokenTTL < c.AuthAccessTokenTTL {
		return fmt.Errorf("STASH_AUTH_REFRESH_TOKEN_TTL must not be shorter than STASH_AUTH_ACCESS_TOKEN_TTL")
	}
	if c.ConsolidationBatchSize <= 0 {
		return fmt.Errorf("STASH_CONSOLIDATION_BATCH_SIZE must be greater than zero")
	}
	if c.ConsolidationSimilarityThreshold < 0 || c.ConsolidationSimilarityThreshold > 1 {
		return fmt.Errorf("STASH_CONSOLIDATION_SIMILARITY_THRESHOLD must be between 0 and 1")
	}
	if c.ConsolidationDedupThreshold < 0 || c.ConsolidationDedupThreshold > 1 {
		return fmt.Errorf("STASH_CONSOLIDATION_DEDUP_THRESHOLD must be between 0 and 1")
	}
	if c.DecayFactor < 0 || c.DecayFactor > 1 {
		return fmt.Errorf("STASH_DECAY_FACTOR must be between 0 and 1")
	}
	if c.ExpiryThreshold < 0 || c.ExpiryThreshold > 1 {
		return fmt.Errorf("STASH_EXPIRY_THRESHOLD must be between 0 and 1")
	}
	if c.HypothesisAutoConfirmThreshold < 0 || c.HypothesisAutoConfirmThreshold > 1 {
		return fmt.Errorf("STASH_HYPOTHESIS_AUTO_CONFIRM_THRESHOLD must be between 0 and 1")
	}
	if c.HypothesisAutoRejectThreshold < 0 || c.HypothesisAutoRejectThreshold > 1 {
		return fmt.Errorf("STASH_HYPOTHESIS_AUTO_REJECT_THRESHOLD must be between 0 and 1")
	}
	return nil
}
