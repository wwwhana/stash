-- +goose Up
-- One project namespace has one shared root goal. The existing goals.parent_id
-- hierarchy decomposes that root into smaller outcomes.
CREATE UNIQUE INDEX goals_id_namespace_id_idx ON goals (id, namespace_id);

CREATE TABLE project_goal_roots (
    namespace_id BIGINT PRIMARY KEY REFERENCES namespaces(id) ON DELETE CASCADE,
    goal_id      BIGINT NOT NULL,
    set_by       TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (goal_id, namespace_id) REFERENCES goals(id, namespace_id) ON DELETE CASCADE
);

-- Durable memory can explain a goal directly, before it is narrowed into
-- executable work. Work-specific memory keeps using work_item_memory_links.
CREATE TABLE goal_memory_links (
    goal_id      BIGINT NOT NULL REFERENCES goals(id) ON DELETE CASCADE,
    memory_type  TEXT NOT NULL CHECK (memory_type IN ('episode', 'fact', 'hypothesis', 'failure')),
    memory_id    BIGINT NOT NULL,
    relation     TEXT NOT NULL CHECK (relation IN ('context', 'constraint', 'decision', 'evidence', 'failure', 'result', 'supersedes')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (goal_id, memory_type, memory_id)
);

CREATE INDEX goal_memory_links_memory_idx ON goal_memory_links (memory_type, memory_id);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stash_validate_project_goal_root()
RETURNS trigger AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM goals goal
        WHERE goal.id = NEW.goal_id
          AND goal.namespace_id = NEW.namespace_id
          AND goal.parent_id IS NULL
          AND goal.status = 'active'
          AND goal.deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'project goal root must be an active top-level goal in the same namespace'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER project_goal_roots_validate_trigger
    BEFORE INSERT OR UPDATE OF namespace_id, goal_id ON project_goal_roots
    FOR EACH ROW EXECUTE FUNCTION stash_validate_project_goal_root();

-- Work assigned after a project root is selected must stay inside that tree.
-- This keeps direct SQL and older clients from starting detached work.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stash_validate_work_item_goal_map()
RETURNS trigger AS $$
DECLARE
    root_goal_id BIGINT;
BEGIN
    SELECT goal_id INTO root_goal_id
    FROM project_goal_roots
    WHERE namespace_id = NEW.namespace_id;

    IF NEW.goal_id IS NULL THEN
        IF root_goal_id IS NOT NULL AND NEW.status IN ('doing', 'review', 'done') THEN
            RAISE EXCEPTION 'active work must belong to the shared project goal tree'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM goals goal
        WHERE goal.id = NEW.goal_id
          AND goal.namespace_id = NEW.namespace_id
          AND goal.status = 'active'
          AND goal.deleted_at IS NULL
    ) THEN
        RAISE EXCEPTION 'work goal must be active and share the work namespace'
            USING ERRCODE = '23514';
    END IF;

    IF root_goal_id IS NOT NULL AND NOT EXISTS (
        WITH RECURSIVE ancestors AS (
            SELECT goal.id, goal.parent_id, ARRAY[goal.id]::BIGINT[] AS path
            FROM goals goal
            WHERE goal.id = NEW.goal_id
              AND goal.namespace_id = NEW.namespace_id
              AND goal.deleted_at IS NULL
            UNION ALL
            SELECT parent.id, parent.parent_id, child.path || parent.id
            FROM goals parent
            JOIN ancestors child ON child.parent_id = parent.id
            WHERE parent.namespace_id = NEW.namespace_id
              AND parent.deleted_at IS NULL
              AND NOT parent.id = ANY(child.path)
        )
        SELECT 1 FROM ancestors WHERE id = root_goal_id
    ) THEN
        RAISE EXCEPTION 'work goal must belong to the shared project goal tree'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER work_items_goal_map_validate_trigger
    BEFORE INSERT OR UPDATE OF namespace_id, goal_id, status ON work_items
    FOR EACH ROW EXECUTE FUNCTION stash_validate_work_item_goal_map();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stash_validate_goal_memory_link()
RETURNS trigger AS $$
DECLARE
    goal_namespace BIGINT;
    memory_namespace BIGINT;
BEGIN
    SELECT namespace_id INTO goal_namespace
    FROM goals
    WHERE id = NEW.goal_id AND deleted_at IS NULL;

    IF goal_namespace IS NULL THEN
        RAISE EXCEPTION 'goal memory link requires an active goal record'
            USING ERRCODE = '23503';
    END IF;

    CASE NEW.memory_type
        WHEN 'episode' THEN
            SELECT namespace_id INTO memory_namespace
            FROM episodes
            WHERE id = NEW.memory_id AND deleted_at IS NULL;
        WHEN 'fact' THEN
            SELECT namespace_id INTO memory_namespace
            FROM facts
            WHERE id = NEW.memory_id AND deleted_at IS NULL AND valid_until IS NULL;
        WHEN 'hypothesis' THEN
            SELECT namespace_id INTO memory_namespace
            FROM hypotheses
            WHERE id = NEW.memory_id AND deleted_at IS NULL;
        WHEN 'failure' THEN
            SELECT namespace_id INTO memory_namespace
            FROM failures
            WHERE id = NEW.memory_id AND deleted_at IS NULL;
        ELSE
            RAISE EXCEPTION 'unsupported goal memory type'
                USING ERRCODE = '23514';
    END CASE;

    IF memory_namespace IS NULL THEN
        RAISE EXCEPTION 'goal memory link requires an active memory record'
            USING ERRCODE = '23503';
    END IF;
    IF memory_namespace <> goal_namespace THEN
        RAISE EXCEPTION 'goal and memory must share a namespace'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER goal_memory_links_validate_trigger
    BEFORE INSERT OR UPDATE OF goal_id, memory_type, memory_id ON goal_memory_links
    FOR EACH ROW EXECUTE FUNCTION stash_validate_goal_memory_link();

-- +goose Down
DROP TRIGGER IF EXISTS goal_memory_links_validate_trigger ON goal_memory_links;
DROP FUNCTION IF EXISTS stash_validate_goal_memory_link();
DROP TRIGGER IF EXISTS work_items_goal_map_validate_trigger ON work_items;
DROP FUNCTION IF EXISTS stash_validate_work_item_goal_map();
DROP TRIGGER IF EXISTS project_goal_roots_validate_trigger ON project_goal_roots;
DROP FUNCTION IF EXISTS stash_validate_project_goal_root();
DROP TABLE IF EXISTS goal_memory_links;
DROP TABLE IF EXISTS project_goal_roots;
DROP INDEX IF EXISTS goals_id_namespace_id_idx;
