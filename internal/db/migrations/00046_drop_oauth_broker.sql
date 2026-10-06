-- +goose Up
-- MCP clients authenticate with API tokens stored in auth_tokens. The OAuth
-- broker that registered clients and rotated refresh tokens is gone, and so
-- is its state.
DROP TABLE IF EXISTS oauth_refresh_tokens;
DROP TABLE IF EXISTS oauth_clients;

-- +goose Down
CREATE TABLE oauth_clients (
    id                         TEXT PRIMARY KEY,
    name                       TEXT NOT NULL DEFAULT '',
    redirect_uris              TEXT[] NOT NULL,
    token_endpoint_auth_method TEXT NOT NULL,
    secret_hash                BYTEA NULL CHECK (secret_hash IS NULL OR octet_length(secret_hash) = 32),
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_used_at               TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE oauth_refresh_tokens (
    token_hash BYTEA PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    subject    TEXT NOT NULL,
    client_id  TEXT NOT NULL,
    resource   TEXT NOT NULL,
    scope      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX oauth_refresh_tokens_grant_idx
    ON oauth_refresh_tokens (subject, client_id, resource, created_at DESC);
CREATE INDEX oauth_refresh_tokens_expiry_idx
    ON oauth_refresh_tokens (expires_at);
