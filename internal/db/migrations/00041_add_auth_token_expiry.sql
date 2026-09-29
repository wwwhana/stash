-- +goose Up
-- Existing tokens keep their unlimited lifetime.
ALTER TABLE auth_tokens ADD COLUMN expires_at TIMESTAMPTZ NULL;

-- +goose Down
ALTER TABLE auth_tokens DROP COLUMN expires_at;
