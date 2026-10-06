-- +goose Up
-- A user is a person; an identity is one way that person proves who they
-- are. Keeping them apart lets one account carry a local password and an SSO
-- subject at the same time, and lets a later SSO provider be linked without
-- touching the user row.
--
-- users.username is the session subject. It scopes namespaces exactly like
-- an API-token subject with the same name, so existing SSO users keep their
-- memory: an identity that arrives without a matching user is provisioned
-- with the upstream subject as its username.
CREATE TABLE users (
    id            BIGSERIAL   PRIMARY KEY,
    username      TEXT        NOT NULL UNIQUE CHECK (username <> '' AND length(username) <= 255),
    display_name  TEXT        NOT NULL DEFAULT '',
    is_admin      BOOLEAN     NOT NULL DEFAULT false,
    disabled      BOOLEAN     NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ NULL
);

-- kind 'password': subject is the username and secret_hash is a bcrypt hash.
-- kind 'oidc': issuer is the provider URL and subject is its stable sub claim.
CREATE TABLE user_identities (
    id           BIGSERIAL   PRIMARY KEY,
    user_id      BIGINT      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind         TEXT        NOT NULL CHECK (kind IN ('password', 'oidc')),
    issuer       TEXT        NOT NULL DEFAULT '',
    subject      TEXT        NOT NULL CHECK (subject <> ''),
    secret_hash  TEXT        NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ NULL,
    UNIQUE (kind, issuer, subject),
    CHECK ((kind = 'password') = (secret_hash IS NOT NULL))
);

CREATE INDEX user_identities_user_idx ON user_identities (user_id, kind);
CREATE UNIQUE INDEX user_identities_one_password_idx ON user_identities (user_id) WHERE kind = 'password';

-- +goose Down
DROP TABLE IF EXISTS user_identities;
DROP TABLE IF EXISTS users;
