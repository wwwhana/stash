-- +goose Up
-- Resolve provenance when read, including facts produced before this migration.
-- Direct links win; source relations never overwrite a deliberate agent link.
CREATE VIEW work_item_memory_context AS
WITH candidates AS (
    SELECT work_item_id, memory_type, memory_id, relation, created_at, false AS derived
    FROM work_item_memory_links
    UNION ALL
    SELECT linked.work_item_id, 'fact', fact.id, linked.relation, linked.created_at, true
    FROM work_item_memory_links linked
    JOIN work_items work ON work.id = linked.work_item_id AND work.deleted_at IS NULL
    JOIN episodes episode ON linked.memory_type = 'episode' AND episode.id = linked.memory_id
    JOIN fact_sources source ON source.episode_id = episode.id
    JOIN facts fact ON fact.id = source.fact_id
    WHERE episode.namespace_id = work.namespace_id AND fact.namespace_id = work.namespace_id
      AND episode.deleted_at IS NULL AND fact.deleted_at IS NULL AND fact.valid_until IS NULL
    UNION ALL
    SELECT linked.work_item_id, 'episode', episode.id, 'context', linked.created_at, true
    FROM work_item_memory_links linked
    JOIN work_items work ON work.id = linked.work_item_id AND work.deleted_at IS NULL
    JOIN facts fact ON linked.memory_type = 'fact' AND fact.id = linked.memory_id
    JOIN fact_sources source ON source.fact_id = fact.id
    JOIN episodes episode ON episode.id = source.episode_id
    WHERE episode.namespace_id = work.namespace_id AND fact.namespace_id = work.namespace_id
      AND episode.deleted_at IS NULL AND fact.deleted_at IS NULL AND fact.valid_until IS NULL
)
SELECT DISTINCT ON (work_item_id, memory_type, memory_id)
    work_item_id, memory_type, memory_id, relation, created_at, derived
FROM candidates
ORDER BY work_item_id, memory_type, memory_id, derived, created_at, relation;

-- +goose Down
DROP VIEW work_item_memory_context;
