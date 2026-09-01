-- +goose Up
ALTER TABLE namespaces ADD COLUMN deleted_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE namespaces DROP COLUMN IF EXISTS deleted_at;
