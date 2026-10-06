-- +goose Up
-- Providers live in the database so an operator can add or switch model
-- servers without redeploying. The API key is sealed by the server
-- (see internal/secrets); the table never holds a readable credential.
CREATE TABLE llm_providers (
    id                      BIGSERIAL   PRIMARY KEY,
    name                    TEXT        NOT NULL UNIQUE,
    kind                    TEXT        NOT NULL DEFAULT 'openai_compatible'
                            CHECK (kind IN ('openai_compatible')),
    base_url                TEXT        NOT NULL,
    api_key_sealed          TEXT        NULL,
    request_timeout_seconds INTEGER     NOT NULL DEFAULT 120 CHECK (request_timeout_seconds > 0),
    enabled                 BOOLEAN     NOT NULL DEFAULT true,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per feature. A missing row means the feature falls back to the
-- STASH_OPENAI_* environment provider when one is configured.
CREATE TABLE llm_feature_assignments (
    feature         TEXT        PRIMARY KEY,
    provider_id     BIGINT      NOT NULL REFERENCES llm_providers(id) ON DELETE RESTRICT,
    model           TEXT        NOT NULL,
    dimensions      INTEGER     NULL CHECK (dimensions IS NULL OR dimensions > 0),
    context_tokens  INTEGER     NOT NULL DEFAULT 0 CHECK (context_tokens >= 0),
    reserved_tokens INTEGER     NOT NULL DEFAULT 0 CHECK (reserved_tokens >= 0),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Every mutation bumps this counter so a running server can notice changes
-- made by another process (the CLI, or a second replica).
INSERT INTO settings (key, value) VALUES ('llm_config_version', '0')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DELETE FROM settings WHERE key = 'llm_config_version';
DROP TABLE IF EXISTS llm_feature_assignments;
DROP TABLE IF EXISTS llm_providers;
