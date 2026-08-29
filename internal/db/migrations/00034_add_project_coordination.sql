-- +goose Up
-- Stash work is not tied to Git. Capabilities route work to a suitable agent,
-- while resources point at the small set of external or Stash-owned material
-- needed to perform that work. Connector-specific credentials and content stay
-- outside these tables.
ALTER TABLE work_items
    ADD CONSTRAINT work_items_id_namespace_id_unique UNIQUE (id, namespace_id);

CREATE TABLE work_item_capabilities (
    work_item_id BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    capability  TEXT NOT NULL
                CHECK (capability ~ '^[a-z][a-z0-9_-]{0,63}$'),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (work_item_id, capability)
);

CREATE INDEX work_item_capabilities_capability_idx
    ON work_item_capabilities (capability, work_item_id);

CREATE TABLE work_resources (
    id              BIGSERIAL PRIMARY KEY,
    namespace_id    BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    resource_key    TEXT NOT NULL CHECK (btrim(resource_key) <> '' AND char_length(resource_key) <= 256),
    kind            TEXT NOT NULL
                    CHECK (kind IN ('git', 'document', 'url', 'browser', 'api', 'dataset', 'device', 'ticket', 'file', 'other')),
    source          TEXT NOT NULL DEFAULT 'stash'
                    CHECK (source ~ '^[a-z][a-z0-9_-]{0,63}$'),
    authority       TEXT NOT NULL DEFAULT 'stash'
                    CHECK (authority IN ('stash', 'external')),
    title           TEXT NOT NULL CHECK (btrim(title) <> '' AND octet_length(title) <= 512),
    uri             TEXT NOT NULL DEFAULT '' CHECK (octet_length(uri) <= 2048),
    summary         TEXT NOT NULL DEFAULT '' CHECK (octet_length(summary) <= 1000),
    external_id     TEXT NOT NULL DEFAULT '' CHECK (octet_length(external_id) <= 256),
    revision        TEXT NOT NULL DEFAULT '' CHECK (octet_length(revision) <= 256),
    content_digest  TEXT NOT NULL DEFAULT ''
                    CHECK (content_digest = '' OR content_digest ~ '^sha256:[0-9a-f]{64}$'),
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb
                    CHECK (jsonb_typeof(metadata) = 'object'),
    created_by      TEXT NOT NULL DEFAULT '' CHECK (octet_length(created_by) <= 256),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ NULL,
    UNIQUE (id, namespace_id)
);

CREATE UNIQUE INDEX work_resources_namespace_key_idx
    ON work_resources (namespace_id, resource_key) WHERE deleted_at IS NULL;
CREATE INDEX work_resources_namespace_source_idx
    ON work_resources (namespace_id, source, updated_at DESC) WHERE deleted_at IS NULL;

CREATE TABLE work_resource_links (
    id            BIGSERIAL PRIMARY KEY,
    namespace_id  BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    work_item_id  BIGINT NOT NULL,
    resource_id   BIGINT NOT NULL,
    role          TEXT NOT NULL
                  CHECK (role IN ('input', 'target', 'output', 'evidence', 'reference')),
    linked_by     TEXT NOT NULL DEFAULT '' CHECK (octet_length(linked_by) <= 256),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (work_item_id, namespace_id)
        REFERENCES work_items(id, namespace_id) ON DELETE CASCADE,
    FOREIGN KEY (resource_id, namespace_id)
        REFERENCES work_resources(id, namespace_id) ON DELETE CASCADE,
    UNIQUE (work_item_id, resource_id, role)
);

CREATE INDEX work_resource_links_item_idx
    ON work_resource_links (work_item_id, created_at DESC, id DESC);
CREATE INDEX work_resource_links_resource_idx
    ON work_resource_links (resource_id, work_item_id);

-- +goose Down
DROP TABLE IF EXISTS work_resource_links;
DROP TABLE IF EXISTS work_resources;
DROP TABLE IF EXISTS work_item_capabilities;
ALTER TABLE work_items DROP CONSTRAINT IF EXISTS work_items_id_namespace_id_unique;
