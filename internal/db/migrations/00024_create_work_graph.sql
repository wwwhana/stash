-- +goose Up
-- Work state is deliberately separate from memory facts. Work items are the
-- mutable operational graph; episodes and facts remain the durable knowledge
-- layer that can be attached to a work item.
CREATE TABLE work_items (
    id              BIGSERIAL PRIMARY KEY,
    namespace_id    BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    goal_id         BIGINT NULL REFERENCES goals(id) ON DELETE SET NULL,
    parent_id       BIGINT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    title           TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'backlog'
                    CHECK (status IN ('backlog', 'ready', 'doing', 'blocked', 'review', 'done', 'canceled')),
    priority        INT NOT NULL DEFAULT 0,
    position        DOUBLE PRECISION NOT NULL DEFAULT 0,
    owner           TEXT NOT NULL DEFAULT '',
    due_at          TIMESTAMPTZ NULL,
    started_at      TIMESTAMPTZ NULL,
    completed_at    TIMESTAMPTZ NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ NULL
);

CREATE INDEX work_items_namespace_status_idx ON work_items (namespace_id, status, position) WHERE deleted_at IS NULL;
CREATE INDEX work_items_goal_idx ON work_items (goal_id) WHERE goal_id IS NOT NULL AND deleted_at IS NULL;
CREATE INDEX work_items_parent_idx ON work_items (parent_id) WHERE parent_id IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE work_item_edges (
    id              BIGSERIAL PRIMARY KEY,
    namespace_id    BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    from_item_id    BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    to_item_id      BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    edge_type       TEXT NOT NULL CHECK (edge_type IN ('blocks', 'relates_to')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ NULL,
    CHECK (from_item_id <> to_item_id)
);

CREATE UNIQUE INDEX work_item_edges_unique_idx
    ON work_item_edges (from_item_id, to_item_id, edge_type) WHERE deleted_at IS NULL;
CREATE INDEX work_item_edges_namespace_idx
    ON work_item_edges (namespace_id, from_item_id, to_item_id) WHERE deleted_at IS NULL;

CREATE TABLE worktrees (
    id              BIGSERIAL PRIMARY KEY,
    namespace_id    BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    repository      TEXT NOT NULL,
    worktree_path   TEXT NOT NULL,
    branch          TEXT NOT NULL DEFAULT '',
    head_sha        TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'unknown'
                    CHECK (status IN ('unknown', 'clean', 'dirty', 'missing', 'merged', 'removed')),
    agent_id        TEXT NOT NULL DEFAULT '',
    last_seen_at    TIMESTAMPTZ NULL,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ NULL,
    UNIQUE (namespace_id, worktree_path)
);

CREATE INDEX worktrees_namespace_idx ON worktrees (namespace_id, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX worktrees_repository_idx ON worktrees (repository) WHERE deleted_at IS NULL;

CREATE TABLE work_item_worktrees (
    work_item_id    BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    worktree_id     BIGINT NOT NULL REFERENCES worktrees(id) ON DELETE CASCADE,
    relation        TEXT NOT NULL DEFAULT 'active'
                    CHECK (relation IN ('active', 'related')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (work_item_id, worktree_id)
);

CREATE TABLE work_events (
    id              BIGSERIAL PRIMARY KEY,
    namespace_id    BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    worktree_id     BIGINT NULL REFERENCES worktrees(id) ON DELETE SET NULL,
    work_item_id    BIGINT NULL REFERENCES work_items(id) ON DELETE SET NULL,
    event_type      TEXT NOT NULL,
    event_key       TEXT NULL,
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX work_events_key_idx
    ON work_events (worktree_id, event_key) WHERE event_key IS NOT NULL;
CREATE INDEX work_events_namespace_idx ON work_events (namespace_id, occurred_at DESC);
CREATE INDEX work_events_worktree_idx ON work_events (worktree_id, occurred_at DESC) WHERE worktree_id IS NOT NULL;
CREATE INDEX work_events_work_item_idx ON work_events (work_item_id, occurred_at DESC) WHERE work_item_id IS NOT NULL;

CREATE TABLE work_item_memory_links (
    work_item_id    BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    memory_type     TEXT NOT NULL CHECK (memory_type IN ('episode', 'fact', 'hypothesis', 'failure', 'goal')),
    memory_id       BIGINT NOT NULL,
    relation        TEXT NOT NULL DEFAULT 'context',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (work_item_id, memory_type, memory_id)
);

CREATE INDEX work_item_memory_links_memory_idx
    ON work_item_memory_links (memory_type, memory_id);

-- +goose Down
DROP TABLE IF EXISTS work_item_memory_links;
DROP TABLE IF EXISTS work_events;
DROP TABLE IF EXISTS work_item_worktrees;
DROP TABLE IF EXISTS worktrees;
DROP TABLE IF EXISTS work_item_edges;
DROP TABLE IF EXISTS work_items;
