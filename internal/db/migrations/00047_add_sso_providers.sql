-- +goose Up
-- OIDC issuers people can sign in with. The environment variables
-- (STASH_AUTH_ISSUER and friends) register the first row at startup; later
-- rows come from the console or `stash sso`. The client secret is sealed
-- with STASH_SECRETS_KEY, so a database read cannot recover it.
CREATE TABLE sso_providers (
    id                   BIGSERIAL   PRIMARY KEY,
    slug                 TEXT        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    display_name         TEXT        NOT NULL DEFAULT '',
    issuer               TEXT        NOT NULL CHECK (issuer <> ''),
    client_id            TEXT        NOT NULL CHECK (client_id <> ''),
    client_secret_sealed TEXT        NOT NULL DEFAULT '',
    redirect_url         TEXT        NOT NULL CHECK (redirect_url <> ''),
    enabled              BOOLEAN     NOT NULL DEFAULT true,
    source               TEXT        NOT NULL DEFAULT 'console' CHECK (source IN ('environment', 'console')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS sso_providers;
