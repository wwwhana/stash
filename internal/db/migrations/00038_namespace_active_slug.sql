-- +goose Up
ALTER TABLE namespaces DROP CONSTRAINT IF EXISTS namespaces_slug_key;
CREATE UNIQUE INDEX IF NOT EXISTS namespaces_active_slug_key
    ON namespaces (slug)
    WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS namespaces_active_slug_key;
ALTER TABLE namespaces ADD CONSTRAINT namespaces_slug_key UNIQUE (slug);
