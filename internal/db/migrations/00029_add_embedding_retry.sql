-- +goose Up
-- Keep failed embedding work durable. Rows with a NULL embedding remain the
-- source of truth; these fields only track when and how the server should try
-- indexing them again.
ALTER TABLE episodes
    ADD COLUMN embedding_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN embedding_last_error TEXT NULL,
    ADD COLUMN embedding_retry_at TIMESTAMPTZ NULL,
    ADD COLUMN embedding_updated_at TIMESTAMPTZ NULL;

ALTER TABLE facts
    ADD COLUMN embedding_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN embedding_last_error TEXT NULL,
    ADD COLUMN embedding_retry_at TIMESTAMPTZ NULL,
    ADD COLUMN embedding_updated_at TIMESTAMPTZ NULL;

CREATE INDEX episodes_embedding_retry_idx
    ON episodes (embedding_retry_at, id)
    WHERE embedding IS NULL AND deleted_at IS NULL;

CREATE INDEX facts_embedding_retry_idx
    ON facts (embedding_retry_at, id)
    WHERE embedding IS NULL AND deleted_at IS NULL;

-- Existing rows without vectors become immediately eligible. This also repairs
-- rows created by older releases that intentionally stored NULL embeddings.
UPDATE episodes
SET embedding_retry_at = now()
WHERE embedding IS NULL AND deleted_at IS NULL;

UPDATE facts
SET embedding_retry_at = now()
WHERE embedding IS NULL AND deleted_at IS NULL;

-- +goose Down
DROP INDEX IF EXISTS facts_embedding_retry_idx;
DROP INDEX IF EXISTS episodes_embedding_retry_idx;

ALTER TABLE facts
    DROP COLUMN IF EXISTS embedding_updated_at,
    DROP COLUMN IF EXISTS embedding_retry_at,
    DROP COLUMN IF EXISTS embedding_last_error,
    DROP COLUMN IF EXISTS embedding_attempts;

ALTER TABLE episodes
    DROP COLUMN IF EXISTS embedding_updated_at,
    DROP COLUMN IF EXISTS embedding_retry_at,
    DROP COLUMN IF EXISTS embedding_last_error,
    DROP COLUMN IF EXISTS embedding_attempts;
