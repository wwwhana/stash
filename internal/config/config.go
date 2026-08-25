package config

import (
	"fmt"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	// Store (PostgreSQL only)
	StoreDSN      string `env:"STASH_POSTGRES_DSN,required"`
	VectorDim     int    `env:"STASH_VECTOR_DIM,required"`
	MaxResultSize int    `env:"STASH_MAX_RESULT_SIZE,required"`

	// OpenAI (embeddings + reasoning)
	OpenAIAPIKey   string `env:"STASH_OPENAI_API_KEY" envDefault:""`
	OpenAIBaseURL  string `env:"STASH_OPENAI_BASE_URL,required"`
	EmbeddingModel string `env:"STASH_EMBEDDING_MODEL,required"`
	ReasonerModel  string `env:"STASH_REASONER_MODEL,required"`

	// Memory
	ContextTTL time.Duration `env:"STASH_CONTEXT_TTL,required"`

	// Server
	HTTPAddr  string `env:"STASH_HTTP_ADDR,required"`
	LogLevel  string `env:"STASH_LOG_LEVEL,required"`
	LogFormat string `env:"STASH_LOG_FORMAT,required"`

	// Authentication
	AuthMode           string        `env:"STASH_AUTH_MODE" envDefault:"none"`
	AuthIssuer         string        `env:"STASH_AUTH_ISSUER" envDefault:""`
	AuthClientID       string        `env:"STASH_AUTH_CLIENT_ID" envDefault:""`
	AuthMCPClientID    string        `env:"STASH_AUTH_MCP_CLIENT_ID" envDefault:""`
	AuthClientSecret   string        `env:"STASH_AUTH_CLIENT_SECRET" envDefault:""`
	AuthRedirectURL    string        `env:"STASH_AUTH_REDIRECT_URL" envDefault:""`
	AuthAPISecret      string        `env:"STASH_AUTH_API_SECRET" envDefault:""`
	AuthMCPResourceURL string        `env:"STASH_AUTH_MCP_RESOURCE_URL" envDefault:""`
	AuthCookieSecure   bool          `env:"STASH_AUTH_COOKIE_SECURE" envDefault:"true"`
	AuthTokenTTL       time.Duration `env:"STASH_AUTH_TOKEN_TTL" envDefault:"720h"`

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
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate rejects configurations that would otherwise fail much later during
// database startup or the first MCP call.
func (c *Config) Validate() error {
	if c.VectorDim <= 0 {
		return fmt.Errorf("STASH_VECTOR_DIM must be greater than zero")
	}
	if c.MaxResultSize <= 0 {
		return fmt.Errorf("STASH_MAX_RESULT_SIZE must be greater than zero")
	}
	if c.ContextTTL <= 0 {
		return fmt.Errorf("STASH_CONTEXT_TTL must be greater than zero")
	}
	if c.HTTPAddr == "" {
		return fmt.Errorf("STASH_HTTP_ADDR must not be empty")
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
