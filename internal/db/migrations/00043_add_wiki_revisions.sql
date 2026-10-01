-- +goose Up
-- Wiki edits, tombstones and restores append revisions; sources stay in episodes.
CREATE TABLE wiki_revisions (
    namespace_id BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    slug TEXT NOT NULL,
    revision BIGINT NOT NULL CHECK (revision > 0),
    title TEXT NOT NULL,
    markdown TEXT NOT NULL,
    source_episode_ids BIGINT[] NOT NULL DEFAULT '{}',
    related_slugs TEXT[] NOT NULL DEFAULT '{}',
    deleted BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace_id, slug, revision)
);

-- +goose Down
DROP TABLE wiki_revisions;
