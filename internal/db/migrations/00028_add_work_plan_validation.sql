-- +goose Up
-- Keep the latest semantic plan review beside the shared plan state. Structural
-- convention warnings are computed directly and are not stored here.
CREATE TABLE work_plan_validations (
    namespace_id  BIGINT PRIMARY KEY REFERENCES namespaces(id) ON DELETE CASCADE,
    model         TEXT NOT NULL,
    plan_digest   TEXT NOT NULL,
    passed        BOOLEAN NOT NULL,
    summary       TEXT NOT NULL DEFAULT '',
    findings      JSONB NOT NULL DEFAULT '[]'::jsonb,
    checked_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX work_plan_validations_checked_at_idx
    ON work_plan_validations (checked_at DESC);

-- +goose Down
DROP TABLE IF EXISTS work_plan_validations;
