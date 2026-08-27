-- +goose Up
-- A work plan is a projection over the existing work graph. Components and
-- their executable child tasks retain normal issue keys, while plan-only
-- metadata remains separate from ordinary local issues.
ALTER TABLE work_items DROP CONSTRAINT IF EXISTS work_items_issue_type_check;
ALTER TABLE work_items
    ADD CONSTRAINT work_items_issue_type_check
    CHECK (issue_type IN ('task', 'bug', 'feature', 'chore', 'question', 'component'));

CREATE TABLE work_plan_items (
    work_item_id       BIGINT PRIMARY KEY REFERENCES work_items(id) ON DELETE CASCADE,
    kind               TEXT NOT NULL CHECK (kind IN ('component', 'task')),
    technical_details  TEXT NOT NULL DEFAULT '',
    owned_paths        TEXT[] NOT NULL DEFAULT '{}'::text[],
    provenance         TEXT NOT NULL DEFAULT '' CHECK (provenance IN ('', 'agent', 'roadmap')),
    started_by         TEXT NOT NULL DEFAULT '',
    completed_by       TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX work_plan_items_kind_idx ON work_plan_items (kind);
CREATE INDEX work_plan_items_paths_idx ON work_plan_items USING GIN (owned_paths);

CREATE TABLE work_plan_decisions (
    id                BIGSERIAL PRIMARY KEY,
    namespace_id      BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    component_id      BIGINT NULL REFERENCES work_items(id) ON DELETE SET NULL,
    work_item_id      BIGINT NULL REFERENCES work_items(id) ON DELETE SET NULL,
    title             TEXT NOT NULL,
    rationale         TEXT NOT NULL DEFAULT '',
    author            TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at        TIMESTAMPTZ NULL
);

CREATE INDEX work_plan_decisions_namespace_idx
    ON work_plan_decisions (namespace_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX work_plan_decisions_component_idx
    ON work_plan_decisions (component_id, created_at DESC) WHERE component_id IS NOT NULL AND deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS work_plan_decisions;
DROP TABLE IF EXISTS work_plan_items;

UPDATE work_items SET issue_type = 'feature' WHERE issue_type = 'component';
ALTER TABLE work_items DROP CONSTRAINT IF EXISTS work_items_issue_type_check;
ALTER TABLE work_items
    ADD CONSTRAINT work_items_issue_type_check
    CHECK (issue_type IN ('task', 'bug', 'feature', 'chore', 'question'));
