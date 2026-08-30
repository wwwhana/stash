-- +goose Up
-- A resume cursor remembers only fact identities and state digests. Raw fact
-- content stays in the facts table and is read only for the bounded response
-- page that needs it.
CREATE TABLE work_context_cursors (
    id              BIGSERIAL PRIMARY KEY,
    work_item_id    BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    principal_id    TEXT NOT NULL DEFAULT '' CHECK (octet_length(principal_id) <= 256),
    context_digest  TEXT NOT NULL CHECK (context_digest ~ '^sha256:[0-9a-f]{64}$'),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (work_item_id, principal_id, context_digest)
);

CREATE INDEX work_context_cursors_latest_idx
    ON work_context_cursors (work_item_id, principal_id, created_at DESC, id DESC);

CREATE TABLE work_context_cursor_facts (
    cursor_id       BIGINT NOT NULL REFERENCES work_context_cursors(id) ON DELETE CASCADE,
    fact_id         BIGINT NOT NULL,
    relation        TEXT NOT NULL CHECK (btrim(relation) <> '' AND octet_length(relation) <= 64),
    status          TEXT NOT NULL CHECK (status IN ('active', 'superseded', 'deleted', 'missing')),
    state_digest    TEXT NOT NULL CHECK (state_digest ~ '^sha256:[0-9a-f]{64}$'),
    PRIMARY KEY (cursor_id, fact_id)
);

CREATE INDEX work_context_cursor_facts_fact_idx
    ON work_context_cursor_facts (fact_id, cursor_id);

-- +goose Down
DROP TABLE IF EXISTS work_context_cursor_facts;
DROP TABLE IF EXISTS work_context_cursors;
