-- +goose Up
-- Keep the retry schedule separate from the short lease used while a worker
-- is calling the provider. Maintenance can wake scheduled failures without
-- stealing work that another worker is currently processing.
ALTER TABLE episodes
    ADD COLUMN embedding_lease_until TIMESTAMPTZ NULL;

ALTER TABLE facts
    ADD COLUMN embedding_lease_until TIMESTAMPTZ NULL;

-- +goose Down
ALTER TABLE facts
    DROP COLUMN IF EXISTS embedding_lease_until;

ALTER TABLE episodes
    DROP COLUMN IF EXISTS embedding_lease_until;
