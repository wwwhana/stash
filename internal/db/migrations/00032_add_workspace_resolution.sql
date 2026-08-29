-- +goose Up
-- A repository binding answers "which project namespace owns this checkout?".
-- A repository instance distinguishes separate clones of the same remote. The
-- instance ID is generated once by the local bridge and stored in local Git
-- config, so moving the checkout does not change its identity.
CREATE TABLE workspace_repositories (
    id                       BIGSERIAL PRIMARY KEY,
    namespace_id             BIGINT NOT NULL REFERENCES namespaces(id) ON DELETE CASCADE,
    repository_instance_id   TEXT NOT NULL,
    provider                 TEXT NOT NULL DEFAULT '',
    provider_repository_id   TEXT NOT NULL DEFAULT '',
    remote_url               TEXT NOT NULL DEFAULT '',
    remote_fingerprint       TEXT NOT NULL DEFAULT '',
    git_common_dir           TEXT NOT NULL DEFAULT '',
    last_seen_at             TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    metadata                 JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at               TIMESTAMPTZ NULL,
    CHECK (btrim(repository_instance_id) <> ''),
    CHECK (remote_fingerprint = '' OR remote_fingerprint ~ '^sha256:[0-9a-f]{64}$')
);

CREATE UNIQUE INDEX workspace_repositories_instance_idx
    ON workspace_repositories (namespace_id, repository_instance_id)
    WHERE deleted_at IS NULL;
CREATE INDEX workspace_repositories_provider_idx
    ON workspace_repositories (provider, provider_repository_id)
    WHERE deleted_at IS NULL AND provider <> '' AND provider_repository_id <> '';
CREATE INDEX workspace_repositories_remote_idx
    ON workspace_repositories (remote_fingerprint)
    WHERE deleted_at IS NULL AND remote_fingerprint <> '';

ALTER TABLE worktrees
    ADD COLUMN workspace_repository_id BIGINT NULL REFERENCES workspace_repositories(id) ON DELETE SET NULL,
    ADD COLUMN worktree_key TEXT NULL,
    ADD COLUMN git_dir TEXT NOT NULL DEFAULT '',
    ADD COLUMN worktree_slot TEXT NOT NULL DEFAULT '',
    ADD COLUMN stale_at TIMESTAMPTZ NULL,
    ADD COLUMN missing_since TIMESTAMPTZ NULL,
    ADD COLUMN removed_at TIMESTAMPTZ NULL;

-- The original table-wide UNIQUE constraint also reserves paths of soft-deleted
-- rows. Replace it with the same active-row semantics used by worktree_key so a
-- removed worktree path can later be reused by a genuinely new worktree.
ALTER TABLE worktrees DROP CONSTRAINT IF EXISTS worktrees_namespace_id_worktree_path_key;
CREATE UNIQUE INDEX worktrees_namespace_path_idx
    ON worktrees (namespace_id, worktree_path) WHERE deleted_at IS NULL;

ALTER TABLE worktrees DROP CONSTRAINT IF EXISTS worktrees_status_check;
ALTER TABLE worktrees
    ADD CONSTRAINT worktrees_status_check
    CHECK (status IN ('unknown', 'clean', 'dirty', 'stale', 'missing', 'merged', 'removed'));
ALTER TABLE worktrees
    ADD CONSTRAINT worktrees_key_format_check
    CHECK (worktree_key IS NULL OR worktree_key ~ '^wtk_v1_[0-9a-f]{64}$');

CREATE UNIQUE INDEX worktrees_namespace_key_idx
    ON worktrees (namespace_id, worktree_key)
    WHERE deleted_at IS NULL AND worktree_key IS NOT NULL;
CREATE INDEX worktrees_workspace_repository_idx
    ON worktrees (workspace_repository_id, last_seen_at DESC)
    WHERE deleted_at IS NULL AND workspace_repository_id IS NOT NULL;

-- resolve_workspace returns one active item for a worktree. Keep that answer
-- unambiguous and reject two agents trying to claim different cards in the
-- same checkout at the database boundary.
WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY worktree_id
               ORDER BY (lease_expires_at > clock_timestamp()) DESC,
                        started_at DESC, id DESC
           ) AS position
    FROM work_attempts
    WHERE status = 'active' AND worktree_id IS NOT NULL
), expired AS (
    UPDATE work_attempts attempt
       SET status = 'expired', ended_at = clock_timestamp(), updated_at = now()
     WHERE attempt.status = 'active'
       AND (
           attempt.lease_expires_at <= clock_timestamp()
           OR attempt.id IN (SELECT id FROM ranked WHERE position > 1)
       )
     RETURNING attempt.id
)
UPDATE work_attempt_lease_tokens token
   SET revoked_at = clock_timestamp()
 WHERE token.attempt_id IN (SELECT id FROM expired) AND token.revoked_at IS NULL;

-- Older versions could leave a card in doing after its lease expired. Repair
-- that state before enforcing the new checkout-level claim invariant.
UPDATE work_items item
   SET status = CASE WHEN EXISTS (
           SELECT 1 FROM work_item_edges edge
           JOIN work_items blocker ON blocker.id = edge.from_item_id
           WHERE edge.to_item_id = item.id AND edge.edge_type = 'blocks'
             AND edge.deleted_at IS NULL AND blocker.deleted_at IS NULL
             AND blocker.status NOT IN ('done', 'canceled')
       ) THEN 'blocked' ELSE 'ready' END,
       owner = '', updated_at = now()
 WHERE item.status = 'doing'
   AND NOT EXISTS (
       SELECT 1 FROM work_attempts attempt
       WHERE attempt.work_item_id = item.id AND attempt.status = 'active'
   );

CREATE UNIQUE INDEX work_attempts_one_active_per_worktree_idx
    ON work_attempts (worktree_id)
    WHERE status = 'active' AND worktree_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS work_attempts_one_active_per_worktree_idx;
DROP INDEX IF EXISTS worktrees_workspace_repository_idx;
DROP INDEX IF EXISTS worktrees_namespace_key_idx;
DROP INDEX IF EXISTS worktrees_namespace_path_idx;
ALTER TABLE worktrees DROP CONSTRAINT IF EXISTS worktrees_key_format_check;
ALTER TABLE worktrees DROP CONSTRAINT IF EXISTS worktrees_status_check;
ALTER TABLE worktrees
    ADD CONSTRAINT worktrees_status_check
    CHECK (status IN ('unknown', 'clean', 'dirty', 'missing', 'merged', 'removed'));
ALTER TABLE worktrees
    DROP COLUMN IF EXISTS removed_at,
    DROP COLUMN IF EXISTS missing_since,
    DROP COLUMN IF EXISTS stale_at,
    DROP COLUMN IF EXISTS worktree_slot,
    DROP COLUMN IF EXISTS git_dir,
    DROP COLUMN IF EXISTS worktree_key,
    DROP COLUMN IF EXISTS workspace_repository_id;
ALTER TABLE worktrees ADD CONSTRAINT worktrees_namespace_id_worktree_path_key UNIQUE (namespace_id, worktree_path);
DROP TABLE IF EXISTS workspace_repositories;
