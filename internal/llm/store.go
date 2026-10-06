package llm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/secrets"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// KindOpenAICompatible is the only provider protocol today. The column is
	// constrained so a future kind is an explicit migration, not a typo.
	KindOpenAICompatible = "openai_compatible"

	// DefaultReservedTokens matches the STASH_REASONER_RESERVED_TOKENS default
	// and applies when an assignment leaves reserved_tokens at zero.
	DefaultReservedTokens = 4096
	// MaxVectorDimensions is the pgvector limit for indexed vector columns.
	MaxVectorDimensions = 2000

	defaultRequestTimeoutSeconds = 120
	apiKeyPurpose                = "llm_provider.api_key"
	versionSettingKey            = "llm_config_version"
)

var (
	ErrProviderNotFound   = errors.New("llm: provider not found")
	ErrProviderInUse      = errors.New("llm: provider is still assigned to a feature")
	ErrProviderNameTaken  = errors.New("llm: provider name is already used")
	ErrInvalidProvider    = errors.New("llm: invalid provider")
	ErrInvalidAssignment  = errors.New("llm: invalid assignment")
	ErrUnknownFeature     = errors.New("llm: unknown feature")
	ErrSecretsKeyRequired = errors.New("llm: STASH_SECRETS_KEY must be configured before an API key can be stored")

	providerNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// Provider is a model server reachable through one base URL. The API key is
// never returned; HasAPIKey only says whether one is stored.
type Provider struct {
	ID                    int64     `json:"id"`
	Name                  string    `json:"name"`
	Kind                  string    `json:"kind"`
	BaseURL               string    `json:"base_url"`
	HasAPIKey             bool      `json:"has_api_key"`
	RequestTimeoutSeconds int       `json:"request_timeout_seconds"`
	Enabled               bool      `json:"enabled"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// Timeout returns the per-request deadline for this provider.
func (p Provider) Timeout() time.Duration {
	return time.Duration(p.RequestTimeoutSeconds) * time.Second
}

// ProviderInput carries a create or partial update. A nil field keeps the
// stored value; an empty APIKey clears it.
type ProviderInput struct {
	Name                  *string `json:"name"`
	Kind                  *string `json:"kind"`
	BaseURL               *string `json:"base_url"`
	APIKey                *string `json:"api_key"`
	RequestTimeoutSeconds *int    `json:"request_timeout_seconds"`
	Enabled               *bool   `json:"enabled"`
}

// Assignment binds one feature to a provider and model.
type Assignment struct {
	Feature        Feature   `json:"feature"`
	ProviderID     int64     `json:"provider_id"`
	Model          string    `json:"model"`
	Dimensions     int       `json:"dimensions,omitempty"`
	ContextTokens  int       `json:"context_tokens"`
	ReservedTokens int       `json:"reserved_tokens"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Store persists providers and assignments. Credentials pass through the
// keyring on the way in and out; a nil keyring still allows key-less
// providers such as a local Ollama.
type Store struct {
	pool    *pgxpool.Pool
	keyring *secrets.Keyring
}

// NewStore wires the provider tables to a pool and an optional keyring.
func NewStore(pool *pgxpool.Pool, keyring *secrets.Keyring) *Store {
	return &Store{pool: pool, keyring: keyring}
}

// CanStoreSecrets reports whether an API key could be sealed right now.
func (s *Store) CanStoreSecrets() bool {
	return s != nil && s.keyring != nil
}

const providerColumns = `id, name, kind, base_url, api_key_sealed IS NOT NULL, request_timeout_seconds, enabled, created_at, updated_at`

func scanProvider(row pgx.Row) (Provider, error) {
	var p Provider
	err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.BaseURL, &p.HasAPIKey, &p.RequestTimeoutSeconds, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrProviderNotFound
	}
	return p, err
}

// ListProviders returns every provider ordered by name.
func (s *Store) ListProviders(ctx context.Context) ([]Provider, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+providerColumns+` FROM llm_providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list llm providers: %w", err)
	}
	defer rows.Close()
	providers := []Provider{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("scan llm provider: %w", err)
		}
		providers = append(providers, p)
	}
	return providers, rows.Err()
}

// GetProvider returns one provider by ID.
func (s *Store) GetProvider(ctx context.Context, id int64) (Provider, error) {
	return scanProvider(s.pool.QueryRow(ctx, `SELECT `+providerColumns+` FROM llm_providers WHERE id = $1`, id))
}

// FindProvider resolves a numeric ID or a name, which is what the CLI accepts.
func (s *Store) FindProvider(ctx context.Context, ref string) (Provider, error) {
	ref = strings.TrimSpace(ref)
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		return s.GetProvider(ctx, id)
	}
	return scanProvider(s.pool.QueryRow(ctx, `SELECT `+providerColumns+` FROM llm_providers WHERE name = $1`, ref))
}

func validateBaseURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("%w: base_url must be an absolute http(s) URL", ErrInvalidProvider)
	}
	return nil
}

func (s *Store) sealAPIKey(key string) (*string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	if s.keyring == nil {
		return nil, ErrSecretsKeyRequired
	}
	sealed, err := s.keyring.Seal(apiKeyPurpose, key)
	if err != nil {
		return nil, err
	}
	return &sealed, nil
}

// CreateProvider stores a new provider. Name and base URL are required.
func (s *Store) CreateProvider(ctx context.Context, in ProviderInput) (Provider, error) {
	name := strings.TrimSpace(deref(in.Name))
	if !providerNameRe.MatchString(name) {
		return Provider{}, fmt.Errorf("%w: name must be lowercase alphanumeric with hyphens or underscores", ErrInvalidProvider)
	}
	kind := strings.TrimSpace(deref(in.Kind))
	if kind == "" {
		kind = KindOpenAICompatible
	}
	if kind != KindOpenAICompatible {
		return Provider{}, fmt.Errorf("%w: unsupported kind %q", ErrInvalidProvider, kind)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(deref(in.BaseURL)), "/")
	if err := validateBaseURL(baseURL); err != nil {
		return Provider{}, err
	}
	timeout := defaultRequestTimeoutSeconds
	if in.RequestTimeoutSeconds != nil {
		timeout = *in.RequestTimeoutSeconds
	}
	if timeout <= 0 {
		return Provider{}, fmt.Errorf("%w: request_timeout_seconds must be greater than zero", ErrInvalidProvider)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	sealed, err := s.sealAPIKey(deref(in.APIKey))
	if err != nil {
		return Provider{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Provider{}, fmt.Errorf("begin create llm provider: %w", err)
	}
	defer tx.Rollback(ctx)
	provider, err := scanProvider(tx.QueryRow(ctx,
		`INSERT INTO llm_providers (name, kind, base_url, api_key_sealed, request_timeout_seconds, enabled)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+providerColumns,
		name, kind, baseURL, sealed, timeout, enabled,
	))
	if err != nil {
		return Provider{}, mapProviderError(err)
	}
	if err := bumpVersion(ctx, tx); err != nil {
		return Provider{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Provider{}, fmt.Errorf("commit create llm provider: %w", err)
	}
	return provider, nil
}

// UpdateProvider applies the non-nil fields of in to an existing provider.
func (s *Store) UpdateProvider(ctx context.Context, id int64, in ProviderInput) (Provider, error) {
	current, err := s.GetProvider(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	if in.Name != nil {
		current.Name = strings.TrimSpace(*in.Name)
		if !providerNameRe.MatchString(current.Name) {
			return Provider{}, fmt.Errorf("%w: name must be lowercase alphanumeric with hyphens or underscores", ErrInvalidProvider)
		}
	}
	if in.Kind != nil && strings.TrimSpace(*in.Kind) != KindOpenAICompatible {
		return Provider{}, fmt.Errorf("%w: unsupported kind %q", ErrInvalidProvider, *in.Kind)
	}
	if in.BaseURL != nil {
		current.BaseURL = strings.TrimRight(strings.TrimSpace(*in.BaseURL), "/")
		if err := validateBaseURL(current.BaseURL); err != nil {
			return Provider{}, err
		}
	}
	if in.RequestTimeoutSeconds != nil {
		current.RequestTimeoutSeconds = *in.RequestTimeoutSeconds
		if current.RequestTimeoutSeconds <= 0 {
			return Provider{}, fmt.Errorf("%w: request_timeout_seconds must be greater than zero", ErrInvalidProvider)
		}
	}
	if in.Enabled != nil {
		current.Enabled = *in.Enabled
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Provider{}, fmt.Errorf("begin update llm provider: %w", err)
	}
	defer tx.Rollback(ctx)
	if in.APIKey != nil {
		sealed, err := s.sealAPIKey(*in.APIKey)
		if err != nil {
			return Provider{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE llm_providers SET api_key_sealed = $2 WHERE id = $1`, id, sealed); err != nil {
			return Provider{}, fmt.Errorf("update llm provider credential: %w", err)
		}
	}
	provider, err := scanProvider(tx.QueryRow(ctx,
		`UPDATE llm_providers
		 SET name = $2, base_url = $3, request_timeout_seconds = $4, enabled = $5, updated_at = now()
		 WHERE id = $1 RETURNING `+providerColumns,
		id, current.Name, current.BaseURL, current.RequestTimeoutSeconds, current.Enabled,
	))
	if err != nil {
		return Provider{}, mapProviderError(err)
	}
	if err := bumpVersion(ctx, tx); err != nil {
		return Provider{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Provider{}, fmt.Errorf("commit update llm provider: %w", err)
	}
	return provider, nil
}

// DeleteProvider removes a provider that no feature uses.
func (s *Store) DeleteProvider(ctx context.Context, id int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete llm provider: %w", err)
	}
	defer tx.Rollback(ctx)
	var assigned int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM llm_feature_assignments WHERE provider_id = $1`, id).Scan(&assigned); err != nil {
		return fmt.Errorf("check llm provider assignments: %w", err)
	}
	if assigned > 0 {
		return ErrProviderInUse
	}
	tag, err := tx.Exec(ctx, `DELETE FROM llm_providers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete llm provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProviderNotFound
	}
	if err := bumpVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListAssignments returns every stored feature assignment.
func (s *Store) ListAssignments(ctx context.Context) ([]Assignment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT feature, provider_id, model, COALESCE(dimensions, 0), context_tokens, reserved_tokens, created_at, updated_at
		 FROM llm_feature_assignments ORDER BY feature`)
	if err != nil {
		return nil, fmt.Errorf("list llm assignments: %w", err)
	}
	defer rows.Close()
	assignments := []Assignment{}
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.Feature, &a.ProviderID, &a.Model, &a.Dimensions, &a.ContextTokens, &a.ReservedTokens, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan llm assignment: %w", err)
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

// SetAssignment creates or replaces the assignment for a feature.
func (s *Store) SetAssignment(ctx context.Context, a Assignment) (Assignment, error) {
	feature, err := ParseFeature(string(a.Feature))
	if err != nil {
		return Assignment{}, err
	}
	a.Model = strings.TrimSpace(a.Model)
	if a.Model == "" {
		return Assignment{}, fmt.Errorf("%w: model is required", ErrInvalidAssignment)
	}
	switch feature.Kind() {
	case KindEmbedding:
		if a.Dimensions <= 0 || a.Dimensions > MaxVectorDimensions {
			return Assignment{}, fmt.Errorf("%w: embedding dimensions must be between 1 and %d", ErrInvalidAssignment, MaxVectorDimensions)
		}
	default:
		a.Dimensions = 0
	}
	if a.ContextTokens < 0 || a.ReservedTokens < 0 {
		return Assignment{}, fmt.Errorf("%w: token budgets must not be negative", ErrInvalidAssignment)
	}
	if a.ContextTokens > 0 && a.ContextTokens <= a.ReservedTokens {
		return Assignment{}, fmt.Errorf("%w: context_tokens must exceed reserved_tokens", ErrInvalidAssignment)
	}
	if _, err := s.GetProvider(ctx, a.ProviderID); err != nil {
		return Assignment{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Assignment{}, fmt.Errorf("begin set llm assignment: %w", err)
	}
	defer tx.Rollback(ctx)
	var dims *int
	if a.Dimensions > 0 {
		dims = &a.Dimensions
	}
	var stored Assignment
	err = tx.QueryRow(ctx,
		`INSERT INTO llm_feature_assignments (feature, provider_id, model, dimensions, context_tokens, reserved_tokens)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (feature) DO UPDATE SET
		   provider_id = EXCLUDED.provider_id, model = EXCLUDED.model, dimensions = EXCLUDED.dimensions,
		   context_tokens = EXCLUDED.context_tokens, reserved_tokens = EXCLUDED.reserved_tokens, updated_at = now()
		 RETURNING feature, provider_id, model, COALESCE(dimensions, 0), context_tokens, reserved_tokens, created_at, updated_at`,
		string(feature), a.ProviderID, a.Model, dims, a.ContextTokens, a.ReservedTokens,
	).Scan(&stored.Feature, &stored.ProviderID, &stored.Model, &stored.Dimensions, &stored.ContextTokens, &stored.ReservedTokens, &stored.CreatedAt, &stored.UpdatedAt)
	if err != nil {
		return Assignment{}, fmt.Errorf("set llm assignment: %w", err)
	}
	if err := bumpVersion(ctx, tx); err != nil {
		return Assignment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Assignment{}, fmt.Errorf("commit set llm assignment: %w", err)
	}
	return stored, nil
}

// ClearAssignment removes a feature's assignment so it falls back to the
// environment provider. Clearing a feature that has none is not an error.
func (s *Store) ClearAssignment(ctx context.Context, feature Feature) error {
	if _, err := ParseFeature(string(feature)); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin clear llm assignment: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM llm_feature_assignments WHERE feature = $1`, string(feature)); err != nil {
		return fmt.Errorf("clear llm assignment: %w", err)
	}
	if err := bumpVersion(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Version returns the change counter bumped by every mutation.
func (s *Store) Version(ctx context.Context) (int64, error) {
	var raw string
	err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = $1`, versionSettingKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read llm config version: %w", err)
	}
	version, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse llm config version %q: %w", raw, err)
	}
	return version, nil
}

func bumpVersion(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO settings (key, value) VALUES ($1, '1')
		 ON CONFLICT (key) DO UPDATE SET value = (settings.value::bigint + 1)::text, updated_at = now()`,
		versionSettingKey,
	); err != nil {
		return fmt.Errorf("bump llm config version: %w", err)
	}
	return nil
}

// OpenAPIKey returns a provider's plaintext key for a connectivity probe.
// It is never exposed over HTTP; handlers pass it straight to Probe.
func (s *Store) OpenAPIKey(ctx context.Context, id int64) (string, error) {
	var sealed *string
	err := s.pool.QueryRow(ctx, `SELECT api_key_sealed FROM llm_providers WHERE id = $1`, id).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrProviderNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read llm provider credential: %w", err)
	}
	if sealed == nil {
		return "", nil
	}
	return s.keyring.Open(apiKeyPurpose, *sealed)
}

// credentials is a provider with its opened API key, used only by the router.
type credentials struct {
	Provider
	apiKey   string
	keyError error
}

// providerCredentials opens every stored key. A key that cannot be opened
// (missing or rotated keyring) is reported per provider instead of failing
// the whole load, so unaffected features keep working.
func (s *Store) providerCredentials(ctx context.Context) (map[int64]credentials, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+providerColumns+`, api_key_sealed FROM llm_providers`)
	if err != nil {
		return nil, fmt.Errorf("load llm provider credentials: %w", err)
	}
	defer rows.Close()
	out := map[int64]credentials{}
	for rows.Next() {
		var c credentials
		var sealed *string
		if err := rows.Scan(&c.ID, &c.Name, &c.Kind, &c.BaseURL, &c.HasAPIKey, &c.RequestTimeoutSeconds, &c.Enabled, &c.CreatedAt, &c.UpdatedAt, &sealed); err != nil {
			return nil, fmt.Errorf("scan llm provider credentials: %w", err)
		}
		if sealed != nil {
			c.apiKey, c.keyError = s.keyring.Open(apiKeyPurpose, *sealed)
		}
		out[c.ID] = c
	}
	return out, rows.Err()
}

func mapProviderError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrProviderNameTaken
	}
	return fmt.Errorf("store llm provider: %w", err)
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
