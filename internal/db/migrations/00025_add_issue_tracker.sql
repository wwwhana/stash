-- +goose Up
-- Work items also act as local issues. The key is human-readable while the
-- numeric ID remains the stable API identifier.
ALTER TABLE work_items
    ADD COLUMN issue_key  TEXT NOT NULL DEFAULT '',
    ADD COLUMN issue_type TEXT NOT NULL DEFAULT 'task'
        CHECK (issue_type IN ('task', 'bug', 'feature', 'chore', 'question')),
    ADD COLUMN labels     TEXT[] NOT NULL DEFAULT '{}'::text[],
    ADD COLUMN reporter   TEXT NOT NULL DEFAULT '';

UPDATE work_items
SET issue_key = 'W-' || lpad(id::text, 6, '0')
WHERE issue_key = '';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stash_set_work_item_issue_key()
RETURNS trigger AS $$
BEGIN
    IF NEW.issue_key = '' THEN
        NEW.issue_key := 'W-' || lpad(NEW.id::text, 6, '0');
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER work_items_issue_key_trigger
    BEFORE INSERT ON work_items
    FOR EACH ROW EXECUTE FUNCTION stash_set_work_item_issue_key();

CREATE UNIQUE INDEX work_items_issue_key_idx
    ON work_items (issue_key) WHERE issue_key <> '' AND deleted_at IS NULL;
CREATE INDEX work_items_issue_type_idx
    ON work_items (namespace_id, issue_type) WHERE deleted_at IS NULL;
CREATE INDEX work_items_labels_idx
    ON work_items USING GIN (labels) WHERE deleted_at IS NULL;

CREATE TABLE work_item_comments (
    id           BIGSERIAL PRIMARY KEY,
    work_item_id BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    author       TEXT NOT NULL DEFAULT '',
    body         TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at   TIMESTAMPTZ NULL
);

CREATE INDEX work_item_comments_item_idx
    ON work_item_comments (work_item_id, created_at) WHERE deleted_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS work_item_comments;
DROP INDEX IF EXISTS work_items_labels_idx;
DROP INDEX IF EXISTS work_items_issue_type_idx;
DROP INDEX IF EXISTS work_items_issue_key_idx;
DROP TRIGGER IF EXISTS work_items_issue_key_trigger ON work_items;
DROP FUNCTION IF EXISTS stash_set_work_item_issue_key();
ALTER TABLE work_items
    DROP COLUMN IF EXISTS reporter,
    DROP COLUMN IF EXISTS labels,
    DROP COLUMN IF EXISTS issue_type,
    DROP COLUMN IF EXISTS issue_key;
