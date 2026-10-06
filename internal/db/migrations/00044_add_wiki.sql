-- +goose Up
-- The wiki is the human-readable layer over memory. Pages are Markdown kept
-- by agents (or the server's reasoner) and cite the episodes, facts, and work
-- they were compiled from, so a reader can always get back to the evidence.
CREATE TABLE wiki_pages (
    id              BIGSERIAL   PRIMARY KEY,
    namespace_id    BIGINT      NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    slug            TEXT        NOT NULL,
    title           TEXT        NOT NULL,
    kind            TEXT        NOT NULL DEFAULT 'article'
                    CHECK (kind IN ('article', 'index', 'entity', 'decision', 'log')),
    summary         TEXT        NOT NULL DEFAULT '',
    content         TEXT        NOT NULL,
    tags            TEXT[]      NOT NULL DEFAULT '{}',
    revision        INTEGER     NOT NULL DEFAULT 1,
    author          TEXT        NOT NULL DEFAULT '',
    author_kind     TEXT        NOT NULL DEFAULT 'agent'
                    CHECK (author_kind IN ('agent', 'server', 'human')),
    -- search_text keeps trigram search language-neutral and LLM-free, the
    -- same way facts.search_text does.
    search_text     TEXT        GENERATED ALWAYS AS (title || ' ' || summary || ' ' || content) STORED,
    embedding       vector      NULL,
    embedding_model TEXT        NULL,
    embedding_attempts INTEGER  NOT NULL DEFAULT 0,
    embedding_last_error TEXT   NULL,
    embedding_retry_at TIMESTAMPTZ NULL,
    embedding_lease_until TIMESTAMPTZ NULL,
    embedding_updated_at TIMESTAMPTZ NULL,
    -- stale_at is set by lint when a cited source was deleted or superseded.
    stale_at        TIMESTAMPTZ NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at      TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX wiki_pages_namespace_slug_idx
    ON wiki_pages (namespace_id, slug) WHERE deleted_at IS NULL;
CREATE INDEX wiki_pages_namespace_updated_idx
    ON wiki_pages (namespace_id, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX wiki_pages_search_text_trgm_idx
    ON wiki_pages USING gin (search_text gin_trgm_ops) WHERE deleted_at IS NULL;
CREATE INDEX wiki_pages_tags_idx ON wiki_pages USING gin (tags);
CREATE INDEX wiki_pages_embedding_retry_idx
    ON wiki_pages (embedding_retry_at, id) WHERE embedding IS NULL AND deleted_at IS NULL;

-- Every write keeps the previous text so a page can be diffed or restored.
CREATE TABLE wiki_revisions (
    id          BIGSERIAL   PRIMARY KEY,
    page_id     BIGINT      NOT NULL REFERENCES wiki_pages(id) ON DELETE CASCADE,
    revision    INTEGER     NOT NULL,
    title       TEXT        NOT NULL,
    summary     TEXT        NOT NULL DEFAULT '',
    content     TEXT        NOT NULL,
    tags        TEXT[]      NOT NULL DEFAULT '{}',
    author      TEXT        NOT NULL DEFAULT '',
    author_kind TEXT        NOT NULL DEFAULT 'agent',
    change_note TEXT        NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (page_id, revision)
);

-- [[slug]] links parsed from the content. target_page_id is NULL while the
-- target does not exist yet; lint reports those as broken links.
CREATE TABLE wiki_links (
    page_id         BIGINT  NOT NULL REFERENCES wiki_pages(id) ON DELETE CASCADE,
    target_slug     TEXT    NOT NULL,
    target_page_id  BIGINT  NULL REFERENCES wiki_pages(id) ON DELETE SET NULL,
    PRIMARY KEY (page_id, target_slug)
);
CREATE INDEX wiki_links_target_idx ON wiki_links (target_page_id);

-- Citations such as [@fact:12] or [@work:W-000001], plus sources attached
-- explicitly. source_digest records the cited text at citation time so lint
-- can tell when the evidence changed underneath the page.
CREATE TABLE wiki_sources (
    page_id       BIGINT      NOT NULL REFERENCES wiki_pages(id) ON DELETE CASCADE,
    source_type   TEXT        NOT NULL
                  CHECK (source_type IN ('episode', 'fact', 'hypothesis', 'failure', 'goal', 'work', 'page', 'url')),
    source_ref    TEXT        NOT NULL,
    source_id     BIGINT      NULL,
    note          TEXT        NOT NULL DEFAULT '',
    source_digest TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (page_id, source_type, source_ref)
);
CREATE INDEX wiki_sources_source_idx ON wiki_sources (source_type, source_id);

-- Append-only activity for the wiki, readable by people and agents alike.
CREATE TABLE wiki_log (
    id           BIGSERIAL   PRIMARY KEY,
    namespace_id BIGINT      NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    page_id      BIGINT      NULL REFERENCES wiki_pages(id) ON DELETE SET NULL,
    page_slug    TEXT        NOT NULL DEFAULT '',
    action       TEXT        NOT NULL
                 CHECK (action IN ('create', 'update', 'delete', 'restore', 'compile', 'lint')),
    actor        TEXT        NOT NULL DEFAULT '',
    actor_kind   TEXT        NOT NULL DEFAULT 'agent',
    summary      TEXT        NOT NULL DEFAULT '',
    details      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX wiki_log_namespace_idx ON wiki_log (namespace_id, id DESC);

-- +goose Down
DROP TABLE IF EXISTS wiki_log;
DROP TABLE IF EXISTS wiki_sources;
DROP TABLE IF EXISTS wiki_links;
DROP TABLE IF EXISTS wiki_revisions;
DROP TABLE IF EXISTS wiki_pages;
