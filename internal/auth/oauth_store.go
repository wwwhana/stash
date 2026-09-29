package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"
)

// Durable MCP OAuth state. When a token pool is attached, registered clients
// and refresh tokens live in PostgreSQL so a restart or redeploy does not
// force every MCP client through a fresh login. Without a pool the in-memory
// maps remain the only store.

const oauthStoreTimeout = 5 * time.Second

func (p *Provider) persistOAuthClient(ctx context.Context, client oauthClient, secret string) error {
	if p.tokenPool == nil {
		return nil
	}
	var secretHash []byte
	if secret != "" {
		sum := sha256.Sum256([]byte(secret))
		secretHash = sum[:]
	}
	// Opportunistically drop idle clients that no longer hold a live grant,
	// then enforce the same ceiling as the in-memory registry.
	if _, err := p.tokenPool.Exec(ctx, `
		DELETE FROM oauth_clients c
		WHERE c.last_used_at < clock_timestamp() - make_interval(secs => $1)
		  AND NOT EXISTS (
			SELECT 1 FROM oauth_refresh_tokens r
			WHERE r.client_id = c.id AND r.expires_at > clock_timestamp()
		  )
	`, dynamicClientIdleTTL.Seconds()); err != nil {
		return fmt.Errorf("prune OAuth clients: %w", err)
	}
	var count int
	if err := p.tokenPool.QueryRow(ctx, `SELECT count(*) FROM oauth_clients`).Scan(&count); err != nil {
		return fmt.Errorf("count OAuth clients: %w", err)
	}
	if count >= maxOAuthClients {
		return errors.New("client registration limit reached")
	}
	redirectURIs := client.RedirectURIs
	if redirectURIs == nil {
		redirectURIs = []string{}
	}
	_, err := p.tokenPool.Exec(ctx, `
		INSERT INTO oauth_clients (id, name, redirect_uris, token_endpoint_auth_method, secret_hash)
		VALUES ($1, $2, $3, $4, $5)
	`, client.ID, client.Name, redirectURIs, client.TokenEndpointAuthMethod, secretHash)
	if err != nil {
		return fmt.Errorf("store OAuth client: %w", err)
	}
	return nil
}

func (p *Provider) loadOAuthClient(clientID string) (oauthClient, bool) {
	if p.tokenPool == nil || clientID == "" {
		return oauthClient{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), oauthStoreTimeout)
	defer cancel()
	client := oauthClient{ID: clientID, Dynamic: true}
	err := p.tokenPool.QueryRow(ctx, `
		UPDATE oauth_clients SET last_used_at = clock_timestamp()
		WHERE id = $1
		RETURNING name, redirect_uris, token_endpoint_auth_method, secret_hash, last_used_at
	`, clientID).Scan(&client.Name, &client.RedirectURIs, &client.TokenEndpointAuthMethod, &client.SecretHash, &client.LastUsed)
	if err != nil {
		return oauthClient{}, false
	}
	return client, true
}

func (p *Provider) persistRefreshToken(ctx context.Context, raw string, token refreshToken) error {
	hash := sha256.Sum256([]byte(raw))
	tx, err := p.tokenPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM oauth_refresh_tokens WHERE expires_at <= clock_timestamp()`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO oauth_refresh_tokens (token_hash, subject, client_id, resource, scope, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, hash[:], token.Subject, token.ClientID, token.Resource, token.Scope, token.CreatedAt, token.ExpiresAt); err != nil {
		return err
	}
	// Keep only the newest grants per subject/client/resource, matching the
	// in-memory maxRefreshPerClient bound.
	if _, err := tx.Exec(ctx, `
		DELETE FROM oauth_refresh_tokens
		WHERE token_hash IN (
			SELECT token_hash FROM oauth_refresh_tokens
			WHERE subject = $1 AND client_id = $2 AND resource = $3
			ORDER BY created_at DESC
			OFFSET $4
		)
	`, token.Subject, token.ClientID, token.Resource, maxRefreshPerClient); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE oauth_clients SET last_used_at = clock_timestamp() WHERE id = $1`, token.ClientID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p *Provider) lookupRefreshToken(ctx context.Context, raw string) (refreshToken, bool) {
	hash := sha256.Sum256([]byte(raw))
	var token refreshToken
	err := p.tokenPool.QueryRow(ctx, `
		SELECT subject, client_id, resource, scope, created_at, expires_at
		FROM oauth_refresh_tokens
		WHERE token_hash = $1 AND expires_at > clock_timestamp()
	`, hash[:]).Scan(&token.Subject, &token.ClientID, &token.Resource, &token.Scope, &token.CreatedAt, &token.ExpiresAt)
	if err != nil {
		return refreshToken{}, false
	}
	return token, true
}

// consumeRefreshToken deletes the token atomically so a rotated refresh token
// cannot be replayed by a concurrent request.
func (p *Provider) consumeRefreshToken(ctx context.Context, raw string) bool {
	hash := sha256.Sum256([]byte(raw))
	var one int
	err := p.tokenPool.QueryRow(ctx, `
		DELETE FROM oauth_refresh_tokens
		WHERE token_hash = $1 AND expires_at > clock_timestamp()
		RETURNING 1
	`, hash[:]).Scan(&one)
	return err == nil
}
