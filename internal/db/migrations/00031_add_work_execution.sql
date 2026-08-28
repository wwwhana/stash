-- +goose Up
-- Attempts are short-lived leases over durable work items. Plaintext tokens are
-- returned only in start responses; the database stores their hashes in the
-- lease-token table below.
CREATE TABLE work_attempts (
    id                 BIGSERIAL PRIMARY KEY,
    work_item_id       BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    worktree_id        BIGINT NULL REFERENCES worktrees(id) ON DELETE SET NULL,
    attempt_number     INT NOT NULL CHECK (attempt_number > 0),
    agent_id           TEXT NOT NULL DEFAULT '',
    principal_id       TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'active'
                       CHECK (status IN ('active', 'handed_off', 'completed', 'expired')),
    lease_expires_at   TIMESTAMPTZ NOT NULL,
    started_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at           TIMESTAMPTZ NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (work_item_id, attempt_number),
    UNIQUE (id, work_item_id),
    UNIQUE (id, principal_id)
);

CREATE UNIQUE INDEX work_attempts_one_active_idx
    ON work_attempts (work_item_id) WHERE status = 'active';
CREATE INDEX work_attempts_item_latest_idx
    ON work_attempts (work_item_id, attempt_number DESC);
CREATE INDEX work_attempts_stale_lease_idx
    ON work_attempts (lease_expires_at) WHERE status = 'active';

-- A retried start request may have more than one response in flight. Keep each
-- issued hash valid for the same active attempt and verified principal until
-- the attempt is handed off, completed, or expired.
CREATE TABLE work_attempt_lease_tokens (
    attempt_id      BIGINT NOT NULL,
    principal_id    TEXT NOT NULL DEFAULT '',
    token_hash      BYTEA NOT NULL CHECK (octet_length(token_hash) = 32),
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    revoked_at      TIMESTAMPTZ NULL,
    PRIMARY KEY (attempt_id, token_hash),
    FOREIGN KEY (attempt_id, principal_id)
        REFERENCES work_attempts(id, principal_id) ON DELETE CASCADE
);

CREATE INDEX work_attempt_lease_tokens_active_idx
    ON work_attempt_lease_tokens (attempt_id, principal_id)
    WHERE revoked_at IS NULL;

CREATE TABLE work_execution_states (
    work_item_id         BIGINT PRIMARY KEY REFERENCES work_items(id) ON DELETE CASCADE,
    current_next_action  TEXT NOT NULL,
    completion_required_after TIMESTAMPTZ NOT NULL DEFAULT '-infinity'::timestamptz,
    completion_required_attempt_number INT NOT NULL DEFAULT 0 CHECK (completion_required_attempt_number >= 0),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE work_checkpoints (
    id             BIGSERIAL PRIMARY KEY,
    attempt_id     BIGINT NOT NULL REFERENCES work_attempts(id) ON DELETE CASCADE,
    summary        TEXT NOT NULL DEFAULT '',
    result         TEXT NOT NULL DEFAULT '',
    next_action    TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX work_checkpoints_attempt_idx
    ON work_checkpoints (attempt_id, created_at DESC, id DESC);

-- Replacing a work item's conditions supersedes the old rows instead of
-- deleting them, so evidence from earlier attempts keeps its explanation.
CREATE TABLE work_completion_conditions (
    id                       BIGSERIAL PRIMARY KEY,
    work_item_id             BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    kind                     TEXT NOT NULL
                             CHECK (kind IN ('command', 'test', 'http', 'file', 'build', 'ui', 'user', 'custom')),
    description              TEXT NOT NULL,
    verification             JSONB NOT NULL
                             CHECK (jsonb_typeof(verification) = 'object' AND verification <> '{}'::jsonb),
    required                 BOOLEAN NOT NULL DEFAULT TRUE,
    position                 INT NOT NULL DEFAULT 0 CHECK (position >= 0),
    status                   TEXT NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'passed', 'waived')),
    waiver_reason            TEXT NOT NULL DEFAULT '',
    verified_by_attempt_id   BIGINT NULL,
    verified_at              TIMESTAMPTZ NULL,
    superseded_at            TIMESTAMPTZ NULL,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, work_item_id),
    FOREIGN KEY (verified_by_attempt_id, work_item_id)
        REFERENCES work_attempts(id, work_item_id)
);

CREATE UNIQUE INDEX work_completion_conditions_active_position_idx
    ON work_completion_conditions (work_item_id, position) WHERE superseded_at IS NULL;
CREATE INDEX work_completion_conditions_item_idx
    ON work_completion_conditions (work_item_id, required, status) WHERE superseded_at IS NULL;

CREATE TABLE work_evidence (
    id              BIGSERIAL PRIMARY KEY,
    work_item_id    BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    attempt_id      BIGINT NOT NULL,
    evidence_type   TEXT NOT NULL,
    summary         TEXT NOT NULL,
    reference       TEXT NOT NULL DEFAULT '',
    payload         JSONB NOT NULL DEFAULT '{}'::jsonb,
    content_digest  TEXT NOT NULL CHECK (content_digest ~ '^sha256:[0-9a-f]{64}$'),
    principal_id    TEXT NOT NULL DEFAULT '',
    worktree_head_sha TEXT NOT NULL DEFAULT '',
    submitted_at    TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, work_item_id),
    FOREIGN KEY (attempt_id, work_item_id)
        REFERENCES work_attempts(id, work_item_id) ON DELETE CASCADE,
    FOREIGN KEY (attempt_id, principal_id)
        REFERENCES work_attempts(id, principal_id) ON DELETE CASCADE
);

CREATE INDEX work_evidence_item_idx
    ON work_evidence (work_item_id, created_at, id);
CREATE INDEX work_evidence_attempt_idx
    ON work_evidence (attempt_id, created_at, id);

CREATE TABLE work_condition_evidence (
    condition_id          BIGINT NOT NULL,
    evidence_id           BIGINT NOT NULL,
    work_item_id          BIGINT NOT NULL,
    linked_by_attempt_id  BIGINT NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (condition_id, evidence_id),
    FOREIGN KEY (condition_id, work_item_id)
        REFERENCES work_completion_conditions(id, work_item_id) ON DELETE CASCADE,
    FOREIGN KEY (evidence_id, work_item_id)
        REFERENCES work_evidence(id, work_item_id) ON DELETE CASCADE,
    FOREIGN KEY (linked_by_attempt_id, work_item_id)
        REFERENCES work_attempts(id, work_item_id) ON DELETE CASCADE
);

CREATE INDEX work_condition_evidence_evidence_idx
    ON work_condition_evidence (evidence_id, condition_id);

-- Action receipts make retries after an uncertain network result return the
-- first committed result instead of repeating the mutation. action_key stores
-- a server-derived SHA-256 digest, never the caller's raw key. Start receipts
-- do not store plaintext lease tokens; a valid replay adds another hash to the
-- same active attempt so out-of-order responses remain usable.
CREATE TABLE work_action_receipts (
    id              BIGSERIAL PRIMARY KEY,
    work_item_id    BIGINT NOT NULL REFERENCES work_items(id) ON DELETE CASCADE,
    attempt_id      BIGINT NULL,
    action_key      TEXT NOT NULL CHECK (action_key ~ '^sha256:[0-9a-f]{64}$'),
    action_type     TEXT NOT NULL,
    request_hash    BYTEA NOT NULL CHECK (octet_length(request_hash) = 32),
    result_id       BIGINT NULL,
    response        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (work_item_id, action_key),
    FOREIGN KEY (attempt_id, work_item_id)
        REFERENCES work_attempts(id, work_item_id) ON DELETE CASCADE
);

CREATE INDEX work_action_receipts_attempt_idx
    ON work_action_receipts (attempt_id, created_at DESC) WHERE attempt_id IS NOT NULL;

ALTER TABLE work_events
    ADD COLUMN attempt_id BIGINT NULL REFERENCES work_attempts(id) ON DELETE SET NULL;

CREATE INDEX work_events_attempt_idx
    ON work_events (attempt_id, occurred_at DESC, id DESC) WHERE attempt_id IS NOT NULL;
CREATE UNIQUE INDEX work_events_attempt_key_idx
    ON work_events (attempt_id, event_key)
    WHERE attempt_id IS NOT NULL AND event_key IS NOT NULL;

-- Existing issue and plan tools can still change work_items.status directly.
-- Once completion conditions exist, only the execution transaction that has
-- completed the latest attempt with linked evidence may move the item to done.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stash_require_work_execution_finish()
RETURNS trigger AS $$
BEGIN
    IF OLD.status = 'done'
       AND NEW.status IS DISTINCT FROM 'done'
       AND EXISTS (
           SELECT 1 FROM work_execution_states state WHERE state.work_item_id = NEW.id
       ) THEN
        UPDATE work_execution_states
        SET completion_required_after = clock_timestamp(),
            completion_required_attempt_number = coalesce((
                SELECT max(attempt.attempt_number)
                FROM work_attempts attempt
                WHERE attempt.work_item_id = NEW.id
            ), 0),
            updated_at = now()
        WHERE work_item_id = NEW.id;
    END IF;

    IF NEW.status = 'done'
       AND OLD.status IS DISTINCT FROM 'done'
       AND EXISTS (
           SELECT 1 FROM work_execution_states state WHERE state.work_item_id = NEW.id
       )
       AND NOT EXISTS (
           SELECT 1
           FROM work_attempts attempt
           WHERE attempt.work_item_id = NEW.id
             AND attempt.status = 'completed'
             AND attempt.ended_at IS NOT NULL
             AND attempt.ended_at > COALESCE((
                 SELECT state.completion_required_after
                 FROM work_execution_states state
                 WHERE state.work_item_id = NEW.id
             ), '-infinity'::timestamptz)
             AND attempt.attempt_number > COALESCE((
                 SELECT state.completion_required_attempt_number
                 FROM work_execution_states state
                 WHERE state.work_item_id = NEW.id
             ), 0)
             AND attempt.attempt_number = (
                 SELECT max(latest.attempt_number)
                 FROM work_attempts latest
                 WHERE latest.work_item_id = NEW.id
             )
             AND attempt.ended_at >= (
                 SELECT max(condition.updated_at)
                 FROM work_completion_conditions condition
                 WHERE condition.work_item_id = NEW.id AND condition.superseded_at IS NULL
             )
             AND EXISTS (
                 SELECT 1 FROM work_completion_conditions condition
                 WHERE condition.work_item_id = NEW.id
                   AND condition.superseded_at IS NULL AND condition.required
             )
             AND NOT EXISTS (
                 SELECT 1
                 FROM work_completion_conditions condition
                 WHERE condition.work_item_id = NEW.id
                   AND condition.superseded_at IS NULL
                   AND condition.required
                   AND (
                       condition.status NOT IN ('passed', 'waived')
                       OR NOT EXISTS (
                           SELECT 1
                           FROM work_condition_evidence linked
                           JOIN work_evidence evidence ON evidence.id = linked.evidence_id
                           WHERE linked.condition_id = condition.id
                             AND linked.work_item_id = NEW.id
                             AND evidence.work_item_id = NEW.id
                       )
                   )
             )
       ) THEN
        RAISE EXCEPTION 'execution-managed work must be completed through finish_work'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER work_items_execution_finish_trigger
    BEFORE UPDATE OF status ON work_items
    FOR EACH ROW EXECUTE FUNCTION stash_require_work_execution_finish();

-- A live lease owns the doing state. Execution transitions end the attempt
-- before moving the item, so any doing-to-other change that still sees an
-- active attempt is an out-of-band mutation and must fail atomically.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stash_require_active_work_status()
RETURNS trigger AS $$
BEGIN
    IF OLD.status = 'doing'
       AND NEW.status IS DISTINCT FROM 'doing'
       AND EXISTS (
           SELECT 1 FROM work_attempts attempt
           WHERE attempt.work_item_id = OLD.id AND attempt.status = 'active'
       ) THEN
        RAISE EXCEPTION 'work with an active attempt must remain doing'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER work_items_active_attempt_status_trigger
    BEFORE UPDATE OF status ON work_items
    FOR EACH ROW EXECUTE FUNCTION stash_require_active_work_status();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stash_protect_active_work_attempt()
RETURNS trigger AS $$
DECLARE
    is_deletion BOOLEAN;
BEGIN
    IF TG_OP = 'DELETE' THEN
        is_deletion := TRUE;
    ELSE
        is_deletion := OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL;
    END IF;

    IF is_deletion AND EXISTS (
        SELECT 1 FROM work_attempts attempt
        WHERE attempt.work_item_id = OLD.id AND attempt.status = 'active'
    ) THEN
        RAISE EXCEPTION 'work with an active attempt cannot be deleted'
            USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER work_items_active_attempt_delete_trigger
    BEFORE DELETE ON work_items
    FOR EACH ROW EXECUTE FUNCTION stash_protect_active_work_attempt();

CREATE TRIGGER work_items_active_attempt_soft_delete_trigger
    BEFORE UPDATE OF deleted_at ON work_items
    FOR EACH ROW EXECUTE FUNCTION stash_protect_active_work_attempt();

-- +goose Down
DROP TRIGGER IF EXISTS work_items_active_attempt_status_trigger ON work_items;
DROP FUNCTION IF EXISTS stash_require_active_work_status();
DROP TRIGGER IF EXISTS work_items_active_attempt_soft_delete_trigger ON work_items;
DROP TRIGGER IF EXISTS work_items_active_attempt_delete_trigger ON work_items;
DROP FUNCTION IF EXISTS stash_protect_active_work_attempt();
DROP TRIGGER IF EXISTS work_items_execution_finish_trigger ON work_items;
DROP FUNCTION IF EXISTS stash_require_work_execution_finish();
DROP INDEX IF EXISTS work_events_attempt_key_idx;
DROP INDEX IF EXISTS work_events_attempt_idx;
ALTER TABLE work_events DROP COLUMN IF EXISTS attempt_id;
DROP TABLE IF EXISTS work_action_receipts;
DROP TABLE IF EXISTS work_condition_evidence;
DROP TABLE IF EXISTS work_evidence;
DROP TABLE IF EXISTS work_completion_conditions;
DROP TABLE IF EXISTS work_checkpoints;
DROP TABLE IF EXISTS work_execution_states;
DROP TABLE IF EXISTS work_attempt_lease_tokens;
DROP TABLE IF EXISTS work_attempts;
