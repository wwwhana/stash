-- +goose Up
-- Browser-issued API credentials stay valid until a user revokes them. Keep
-- only a SHA-256 digest so a database read cannot recover a usable token.
CREATE TABLE auth_tokens (
    id          BIGSERIAL PRIMARY KEY,
    subject     TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    token_hash  BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_used_at TIMESTAMPTZ NULL,
    revoked_at  TIMESTAMPTZ NULL
);

CREATE INDEX auth_tokens_subject_created_idx
    ON auth_tokens (subject, created_at DESC, id DESC);

-- +goose Down
DROP TABLE auth_tokens;
