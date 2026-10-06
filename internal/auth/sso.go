package auth

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/secrets"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

// SSO providers are the OIDC issuers people can sign in with. They live in
// the sso_providers table so an operator registers them in the console; the
// environment variables remain the way to register the first one, and are
// imported into the table on startup.

const (
	ssoSecretPurpose    = "sso_client_secret"
	ssoSourceEnv        = "environment"
	ssoSourceConsole    = "console"
	ssoErrorRetryPeriod = time.Minute
)

var (
	ssoSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

	ErrSSOProviderNotFound   = errors.New("auth: SSO provider not found")
	ErrSSOSlugTaken          = errors.New("auth: an SSO provider with that slug already exists")
	ErrInvalidSSOProvider    = errors.New("auth: invalid SSO provider")
	ErrSSOSecretsUnavailable = errors.New("auth: STASH_SECRETS_KEY is required to store an SSO client secret")
	ErrSSOUnavailable        = errors.New("auth: SSO providers need STASH_AUTH_MODE=token or oauth and a database")
)

// SSOProvider is one OIDC issuer. The client secret never leaves the auth
// package; HasClientSecret says whether one is stored.
type SSOProvider struct {
	ID              int64     `json:"id"`
	Slug            string    `json:"slug"`
	DisplayName     string    `json:"display_name"`
	Issuer          string    `json:"issuer"`
	ClientID        string    `json:"client_id"`
	HasClientSecret bool      `json:"has_client_secret"`
	RedirectURL     string    `json:"redirect_url"`
	Enabled         bool      `json:"enabled"`
	Source          string    `json:"source"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	// Status is the loaded state: ready, error (with Error), or disabled.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Name is what a login button shows.
func (s SSOProvider) Name() string {
	if name := strings.TrimSpace(s.DisplayName); name != "" {
		return name
	}
	if parsed, err := url.Parse(s.Issuer); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return s.Slug
}

// SSOProviderInput carries a create or update; nil keeps the stored value.
// ClientSecret is required on create and optional on update.
type SSOProviderInput struct {
	Slug         *string `json:"slug"`
	DisplayName  *string `json:"display_name"`
	Issuer       *string `json:"issuer"`
	ClientID     *string `json:"client_id"`
	ClientSecret *string `json:"client_secret"`
	RedirectURL  *string `json:"redirect_url"`
	Enabled      *bool   `json:"enabled"`
}

// SSOOption is what the login page and the console need to offer a button.
type SSOOption struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ssoRuntime is a provider after discovery: the OAuth2 client, the ID-token
// verifiers, and the introspection endpoint, or the error that stopped it.
type ssoRuntime struct {
	SSOProvider
	clientSecret          string
	oauth2Config          oauth2.Config
	verifier              *oidc.IDTokenVerifier
	hmacVerifier          *oidc.IDTokenVerifier
	introspectionEndpoint string
	err                   error
}

func (rt *ssoRuntime) ready() bool { return rt != nil && rt.err == nil && rt.Enabled }

// ssoSet is the atomically swapped view of every loaded provider.
type ssoSet struct {
	providers []*ssoRuntime
	bySlug    map[string]*ssoRuntime
	version   string
	loadedAt  time.Time
}

func (s *ssoSet) ready() []*ssoRuntime {
	if s == nil {
		return nil
	}
	var out []*ssoRuntime
	for _, rt := range s.providers {
		if rt.ready() {
			out = append(out, rt)
		}
	}
	return out
}

func (s *ssoSet) hasError() bool {
	if s == nil {
		return false
	}
	for _, rt := range s.providers {
		if rt.Enabled && rt.err != nil {
			return true
		}
	}
	return false
}

// SetSecrets attaches the keyring that seals client secrets.
func (p *Provider) SetSecrets(keyring *secrets.Keyring) {
	if p != nil {
		p.secrets = keyring
	}
}

// CanStoreSSOSecrets reports whether a client secret can be sealed.
func (p *Provider) CanStoreSSOSecrets() bool {
	return p != nil && p.secrets != nil
}

func (p *Provider) ssoEnabled() bool {
	return p != nil && p.tokenPool != nil && p.Mode() != "stdio"
}

// envSSO is the provider described by the environment, when complete.
func (p *Provider) envSSO() (SSOProvider, string, bool) {
	if p == nil {
		return SSOProvider{}, "", false
	}
	cfg := p.config
	if strings.TrimSpace(cfg.Issuer) == "" || strings.TrimSpace(cfg.ClientID) == "" || cfg.ClientSecret == "" || strings.TrimSpace(cfg.RedirectURL) == "" {
		return SSOProvider{}, "", false
	}
	provider := SSOProvider{
		Slug:            "environment",
		DisplayName:     "",
		Issuer:          strings.TrimSpace(cfg.Issuer),
		ClientID:        strings.TrimSpace(cfg.ClientID),
		HasClientSecret: true,
		RedirectURL:     strings.TrimSpace(cfg.RedirectURL),
		Enabled:         true,
		Source:          ssoSourceEnv,
	}
	return provider, cfg.ClientSecret, true
}

func (p *Provider) currentSSO() *ssoSet {
	if p == nil {
		return nil
	}
	return p.sso.Load()
}

func (p *Provider) ssoBySlug(slug string) *ssoRuntime {
	set := p.currentSSO()
	if set == nil {
		return nil
	}
	return set.bySlug[strings.ToLower(strings.TrimSpace(slug))]
}

// SSOOptions lists the providers that can start a login right now.
func (p *Provider) SSOOptions() []SSOOption {
	options := []SSOOption{}
	for _, rt := range p.currentSSO().ready() {
		options = append(options, SSOOption{Slug: rt.Slug, Name: rt.Name()})
	}
	return options
}

func (p *Provider) browserLoginConfigured() bool {
	return p != nil && len(p.currentSSO().ready()) > 0
}

// ssoRuntimeFor picks the provider for a login: the named one, or the only
// one when the name is omitted.
func (p *Provider) ssoRuntimeFor(slug string) (*ssoRuntime, error) {
	if slug != "" {
		rt := p.ssoBySlug(slug)
		if rt == nil {
			return nil, errors.New("unknown SSO provider")
		}
		if !rt.ready() {
			return nil, errors.New("this SSO provider is not available")
		}
		return rt, nil
	}
	ready := p.currentSSO().ready()
	switch len(ready) {
	case 0:
		return nil, errors.New("SSO login is not configured")
	case 1:
		return ready[0], nil
	default:
		return nil, errors.New("choose an SSO provider")
	}
}

const ssoColumns = `id, slug, display_name, issuer, client_id, client_secret_sealed, redirect_url, enabled, source, created_at, updated_at`

type ssoRow struct {
	SSOProvider
	sealed string
}

func scanSSO(row pgx.Row) (ssoRow, error) {
	var r ssoRow
	err := row.Scan(&r.ID, &r.Slug, &r.DisplayName, &r.Issuer, &r.ClientID, &r.sealed, &r.RedirectURL, &r.Enabled, &r.Source, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrSSOProviderNotFound
	}
	r.HasClientSecret = r.sealed != ""
	return r, err
}

func (p *Provider) loadSSORows(ctx context.Context) ([]ssoRow, error) {
	rows, err := p.tokenPool.Query(ctx, `SELECT `+ssoColumns+` FROM sso_providers ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("list SSO providers: %w", err)
	}
	defer rows.Close()
	var out []ssoRow
	for rows.Next() {
		r, err := scanSSO(rows)
		if err != nil {
			return nil, fmt.Errorf("scan SSO provider: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSSOProviders returns the stored providers with their loaded status.
// The environment provider appears too while it is not imported, with ID 0.
func (p *Provider) ListSSOProviders(ctx context.Context) ([]SSOProvider, error) {
	if !p.ssoEnabled() {
		return nil, ErrSSOUnavailable
	}
	rows, err := p.loadSSORows(ctx)
	if err != nil {
		return nil, err
	}
	set := p.currentSSO()
	out := make([]SSOProvider, 0, len(rows)+1)
	for _, r := range rows {
		out = append(out, p.withStatus(r.SSOProvider, set))
	}
	if env, _, ok := p.envSSO(); ok && !ssoRowsCover(rows, env) {
		out = append(out, p.withStatus(env, set))
	}
	return out, nil
}

func (p *Provider) withStatus(provider SSOProvider, set *ssoSet) SSOProvider {
	provider.Status = "disabled"
	if !provider.Enabled {
		return provider
	}
	provider.Status = "error"
	provider.Error = "not loaded yet"
	if set != nil {
		if rt := set.bySlug[provider.Slug]; rt != nil {
			if rt.err != nil {
				provider.Error = rt.err.Error()
			} else {
				provider.Status = "ready"
				provider.Error = ""
			}
		}
	}
	return provider
}

func ssoRowsCover(rows []ssoRow, env SSOProvider) bool {
	for _, r := range rows {
		if strings.TrimRight(r.Issuer, "/") == strings.TrimRight(env.Issuer, "/") && r.ClientID == env.ClientID {
			return true
		}
	}
	return false
}

// GetSSOProvider returns one stored provider.
func (p *Provider) GetSSOProvider(ctx context.Context, id int64) (SSOProvider, error) {
	if !p.ssoEnabled() {
		return SSOProvider{}, ErrSSOUnavailable
	}
	r, err := scanSSO(p.tokenPool.QueryRow(ctx, `SELECT `+ssoColumns+` FROM sso_providers WHERE id = $1`, id))
	if err != nil {
		return SSOProvider{}, err
	}
	return p.withStatus(r.SSOProvider, p.currentSSO()), nil
}

func (p *Provider) validateSSO(provider SSOProvider) error {
	if !ssoSlugRe.MatchString(provider.Slug) {
		return fmt.Errorf("%w: slug must be 1-64 lowercase letters, digits, hyphens, or underscores", ErrInvalidSSOProvider)
	}
	if !validResourceURL(provider.Issuer) {
		return fmt.Errorf("%w: issuer must be an HTTPS URL (or loopback HTTP)", ErrInvalidSSOProvider)
	}
	if strings.TrimSpace(provider.ClientID) == "" {
		return fmt.Errorf("%w: client ID is required", ErrInvalidSSOProvider)
	}
	if !validRedirectURI(provider.RedirectURL) {
		return fmt.Errorf("%w: redirect URL must be an HTTPS URL (or loopback HTTP)", ErrInvalidSSOProvider)
	}
	redirect, _ := url.Parse(provider.RedirectURL)
	if redirect.Scheme == "https" && !p.config.CookieSecure {
		return fmt.Errorf("%w: an HTTPS redirect URL needs STASH_AUTH_COOKIE_SECURE=true", ErrInvalidSSOProvider)
	}
	if redirect.Scheme == "http" && p.config.CookieSecure {
		return fmt.Errorf("%w: a loopback HTTP redirect URL needs STASH_AUTH_COOKIE_SECURE=false", ErrInvalidSSOProvider)
	}
	return nil
}

func (p *Provider) sealSSOSecret(secret string) (string, error) {
	if p.secrets == nil {
		return "", ErrSSOSecretsUnavailable
	}
	sealed, err := p.secrets.Seal(ssoSecretPurpose, secret)
	if err != nil {
		return "", fmt.Errorf("seal client secret: %w", err)
	}
	return sealed, nil
}

// CreateSSOProvider stores a provider and loads it.
func (p *Provider) CreateSSOProvider(ctx context.Context, in SSOProviderInput) (SSOProvider, error) {
	if !p.ssoEnabled() {
		return SSOProvider{}, ErrSSOUnavailable
	}
	provider := SSOProvider{Enabled: true, Source: ssoSourceConsole}
	applySSOInput(&provider, in)
	if in.ClientSecret == nil || *in.ClientSecret == "" {
		return SSOProvider{}, fmt.Errorf("%w: client secret is required", ErrInvalidSSOProvider)
	}
	if err := p.validateSSO(provider); err != nil {
		return SSOProvider{}, err
	}
	sealed, err := p.sealSSOSecret(*in.ClientSecret)
	if err != nil {
		return SSOProvider{}, err
	}
	return p.insertSSO(ctx, provider, sealed)
}

func (p *Provider) insertSSO(ctx context.Context, provider SSOProvider, sealed string) (SSOProvider, error) {
	r, err := scanSSO(p.tokenPool.QueryRow(ctx, `
		INSERT INTO sso_providers (slug, display_name, issuer, client_id, client_secret_sealed, redirect_url, enabled, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+ssoColumns,
		provider.Slug, strings.TrimSpace(provider.DisplayName), provider.Issuer, provider.ClientID, sealed, provider.RedirectURL, provider.Enabled, provider.Source))
	if isUniqueViolation(err) {
		return SSOProvider{}, ErrSSOSlugTaken
	}
	if err != nil {
		return SSOProvider{}, fmt.Errorf("create SSO provider: %w", err)
	}
	if err := p.ReloadSSO(ctx); err != nil {
		log.Printf("reload SSO providers: %v", err)
	}
	return p.withStatus(r.SSOProvider, p.currentSSO()), nil
}

func applySSOInput(provider *SSOProvider, in SSOProviderInput) {
	if in.Slug != nil {
		provider.Slug = strings.ToLower(strings.TrimSpace(*in.Slug))
	}
	if in.DisplayName != nil {
		provider.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Issuer != nil {
		provider.Issuer = strings.TrimSpace(*in.Issuer)
	}
	if in.ClientID != nil {
		provider.ClientID = strings.TrimSpace(*in.ClientID)
	}
	if in.RedirectURL != nil {
		provider.RedirectURL = strings.TrimSpace(*in.RedirectURL)
	}
	if in.Enabled != nil {
		provider.Enabled = *in.Enabled
	}
}

// UpdateSSOProvider changes a stored provider; an empty client secret keeps
// the stored one.
func (p *Provider) UpdateSSOProvider(ctx context.Context, id int64, in SSOProviderInput) (SSOProvider, error) {
	if !p.ssoEnabled() {
		return SSOProvider{}, ErrSSOUnavailable
	}
	r, err := scanSSO(p.tokenPool.QueryRow(ctx, `SELECT `+ssoColumns+` FROM sso_providers WHERE id = $1`, id))
	if err != nil {
		return SSOProvider{}, err
	}
	provider := r.SSOProvider
	applySSOInput(&provider, in)
	if err := p.validateSSO(provider); err != nil {
		return SSOProvider{}, err
	}
	sealed := r.sealed
	if in.ClientSecret != nil && *in.ClientSecret != "" {
		if sealed, err = p.sealSSOSecret(*in.ClientSecret); err != nil {
			return SSOProvider{}, err
		}
	}
	updated, err := scanSSO(p.tokenPool.QueryRow(ctx, `
		UPDATE sso_providers
		SET slug = $2, display_name = $3, issuer = $4, client_id = $5, client_secret_sealed = $6, redirect_url = $7, enabled = $8, updated_at = now()
		WHERE id = $1 RETURNING `+ssoColumns,
		id, provider.Slug, provider.DisplayName, provider.Issuer, provider.ClientID, sealed, provider.RedirectURL, provider.Enabled))
	if isUniqueViolation(err) {
		return SSOProvider{}, ErrSSOSlugTaken
	}
	if err != nil {
		return SSOProvider{}, fmt.Errorf("update SSO provider: %w", err)
	}
	if err := p.ReloadSSO(ctx); err != nil {
		log.Printf("reload SSO providers: %v", err)
	}
	return p.withStatus(updated.SSOProvider, p.currentSSO()), nil
}

// DeleteSSOProvider removes a stored provider. Users who signed in through
// it keep their accounts; the identity rows stay for a later re-link.
func (p *Provider) DeleteSSOProvider(ctx context.Context, id int64) error {
	if !p.ssoEnabled() {
		return ErrSSOUnavailable
	}
	tag, err := p.tokenPool.Exec(ctx, `DELETE FROM sso_providers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete SSO provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSSOProviderNotFound
	}
	if err := p.ReloadSSO(ctx); err != nil {
		log.Printf("reload SSO providers: %v", err)
	}
	return nil
}

// ImportEnvironmentSSO copies the STASH_AUTH_ISSUER settings into the table
// once. It reports false when the environment has no complete provider or
// the same issuer and client are already stored.
func (p *Provider) ImportEnvironmentSSO(ctx context.Context) (SSOProvider, bool, error) {
	if !p.ssoEnabled() {
		return SSOProvider{}, false, ErrSSOUnavailable
	}
	env, secret, ok := p.envSSO()
	if !ok {
		return SSOProvider{}, false, nil
	}
	rows, err := p.loadSSORows(ctx)
	if err != nil {
		return SSOProvider{}, false, err
	}
	if ssoRowsCover(rows, env) {
		return SSOProvider{}, false, nil
	}
	if err := p.validateSSO(env); err != nil {
		return SSOProvider{}, false, err
	}
	sealed, err := p.sealSSOSecret(secret)
	if err != nil {
		return SSOProvider{}, false, err
	}
	env.Slug = ssoSlugFromIssuer(env.Issuer)
	taken := map[string]bool{}
	for _, r := range rows {
		taken[r.Slug] = true
	}
	base := env.Slug
	for i := 2; taken[env.Slug]; i++ {
		env.Slug = fmt.Sprintf("%s-%d", base, i)
	}
	provider, err := p.insertSSO(ctx, env, sealed)
	if err != nil {
		return SSOProvider{}, false, err
	}
	return provider, true, nil
}

// ssoSlugFromIssuer derives a stable slug such as "auth-example-com".
func ssoSlugFromIssuer(issuer string) string {
	parsed, err := url.Parse(issuer)
	host := ""
	if err == nil {
		host = parsed.Hostname()
	}
	slug := strings.ToLower(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(host), "-"))
	slug = strings.Trim(slug, "-")
	if slug == "" || !ssoSlugRe.MatchString(slug) {
		return "sso"
	}
	if len(slug) > 64 {
		slug = strings.Trim(slug[:64], "-")
	}
	return slug
}

// ReloadSSO rebuilds the loaded providers from the table plus the
// environment (while the latter is not imported). Discovery failures are
// kept per provider so one broken issuer does not take the others down.
func (p *Provider) ReloadSSO(ctx context.Context) error {
	if p == nil || p.Mode() == "stdio" {
		return nil
	}
	p.ssoMu.Lock()
	defer p.ssoMu.Unlock()
	var rows []ssoRow
	version := "none"
	if p.tokenPool != nil {
		var err error
		if rows, err = p.loadSSORows(ctx); err != nil {
			return err
		}
		version, _ = p.ssoVersion(ctx)
	}
	previous := p.currentSSO()
	set := &ssoSet{bySlug: map[string]*ssoRuntime{}, version: version, loadedAt: time.Now()}
	add := func(provider SSOProvider, secret string, secretErr error) {
		rt := &ssoRuntime{SSOProvider: provider, clientSecret: secret, err: secretErr}
		if rt.err == nil && provider.Enabled {
			// Reuse a healthy runtime for an unchanged provider so a reload
			// does not re-run discovery against every issuer.
			var prev *ssoRuntime
			if previous != nil {
				prev = previous.bySlug[provider.Slug]
			}
			if prev != nil && prev.err == nil && ssoSame(prev.SSOProvider, provider) && prev.clientSecret == secret {
				rt = prev
			} else {
				p.discoverSSO(ctx, rt)
			}
		}
		set.providers = append(set.providers, rt)
		set.bySlug[provider.Slug] = rt
	}
	for _, r := range rows {
		secret, err := "", error(nil)
		if r.sealed != "" {
			if p.secrets == nil {
				err = ErrSSOSecretsUnavailable
			} else if secret, err = p.secrets.Open(ssoSecretPurpose, r.sealed); err != nil {
				err = fmt.Errorf("open client secret: %w", err)
			}
		}
		add(r.SSOProvider, secret, err)
	}
	if env, secret, ok := p.envSSO(); ok && !ssoRowsCover(rows, env) {
		add(env, secret, p.validateSSO(env))
	}
	sort.SliceStable(set.providers, func(i, j int) bool { return set.providers[i].Slug < set.providers[j].Slug })
	p.sso.Store(set)
	for _, rt := range set.providers {
		if rt.Enabled && rt.err != nil {
			log.Printf("SSO provider %q is not available: %v", rt.Slug, rt.err)
		}
	}
	return nil
}

func ssoSame(a, b SSOProvider) bool {
	return a.Issuer == b.Issuer && a.ClientID == b.ClientID && a.RedirectURL == b.RedirectURL && a.Enabled == b.Enabled
}

// discoverSSO performs OIDC discovery for one provider and fills in its
// OAuth2 client and verifiers.
func (p *Provider) discoverSSO(ctx context.Context, rt *ssoRuntime) {
	discoveryCtx, cancel := context.WithTimeout(ctx, oauthProviderTimeout)
	defer cancel()
	provider, err := oidc.NewProvider(discoveryCtx, rt.Issuer)
	if err != nil {
		rt.err = fmt.Errorf("discover issuer: %w", err)
		return
	}
	endpoint := provider.Endpoint()
	if !validRedirectURI(endpoint.AuthURL) || !validRedirectURI(endpoint.TokenURL) {
		rt.err = errors.New("issuer returned an unsafe authorization or token endpoint")
		return
	}
	rt.oauth2Config = oauth2.Config{
		ClientID:     rt.ClientID,
		ClientSecret: rt.clientSecret,
		RedirectURL:  rt.RedirectURL,
		Endpoint:     endpoint,
		Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
	}
	rt.verifier = provider.Verifier(&oidc.Config{ClientID: rt.ClientID})
	rt.hmacVerifier = newHMACVerifier(rt.Issuer, rt.ClientID, rt.clientSecret, false)
	rt.introspectionEndpoint = providerIntrospectionEndpoint(provider)
	rt.err = nil
}

// TestSSOProvider runs discovery for a stored provider without changing the
// loaded set, so an operator can check a new issuer before enabling it.
func (p *Provider) TestSSOProvider(ctx context.Context, id int64) error {
	if !p.ssoEnabled() {
		return ErrSSOUnavailable
	}
	r, err := scanSSO(p.tokenPool.QueryRow(ctx, `SELECT `+ssoColumns+` FROM sso_providers WHERE id = $1`, id))
	if err != nil {
		return err
	}
	rt := &ssoRuntime{SSOProvider: r.SSOProvider}
	if r.sealed != "" {
		if p.secrets == nil {
			return ErrSSOSecretsUnavailable
		}
		if rt.clientSecret, err = p.secrets.Open(ssoSecretPurpose, r.sealed); err != nil {
			return fmt.Errorf("open client secret: %w", err)
		}
	}
	p.discoverSSO(ctx, rt)
	return rt.err
}

func (p *Provider) ssoVersion(ctx context.Context) (string, error) {
	var count int64
	var updated time.Time
	err := p.tokenPool.QueryRow(ctx, `SELECT count(*), coalesce(max(updated_at), to_timestamp(0)) FROM sso_providers`).Scan(&count, &updated)
	if err != nil {
		return "", fmt.Errorf("read SSO version: %w", err)
	}
	return fmt.Sprintf("%d:%d", count, updated.UnixNano()), nil
}

// ReloadSSOIfChanged reloads when another process changed the table, or
// when a provider failed discovery and a minute has passed.
func (p *Provider) ReloadSSOIfChanged(ctx context.Context) (bool, error) {
	if !p.ssoEnabled() {
		return false, nil
	}
	set := p.currentSSO()
	version, err := p.ssoVersion(ctx)
	if err != nil {
		return false, err
	}
	if set != nil && set.version == version && !(set.hasError() && time.Since(set.loadedAt) >= ssoErrorRetryPeriod) {
		return false, nil
	}
	return true, p.ReloadSSO(ctx)
}

// installSSO replaces the loaded set with the given runtimes; tests use it
// to skip discovery.
func (p *Provider) installSSO(runtimes ...*ssoRuntime) {
	set := &ssoSet{bySlug: map[string]*ssoRuntime{}, loadedAt: time.Now()}
	for _, rt := range runtimes {
		set.providers = append(set.providers, rt)
		set.bySlug[rt.Slug] = rt
	}
	p.sso.Store(set)
}
