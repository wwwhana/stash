# Work execution call sequence

Tool names can carry a client-specific MCP prefix. Match them by the final Stash tool name.

## Resume and claim

1. `resume_work(work_item_id, recent_event_limit?)` reads the compact recovery bundle. It does not return another attempt's private token.
2. `prepare_work(work_item_id, next_action, conditions, action_key)` replaces the observable completion conditions and stores the first concrete action. Call it only when conditions are absent or intentionally changed. Each condition needs `kind`, a non-empty `description`, and a non-empty `verification` object; `required` defaults to true.
3. `start_work(work_item_id, agent_id, action_key, worktree_id?, lease_seconds?)` creates the exclusive attempt and returns its `attempt_id` and a private `lease_token`. The action key must contain a fresh random UUIDv4. An exact replay returns the same attempt with a fresh valid token; every returned token remains valid only until that attempt is handed off, completed, or expires.

Do not begin tracked work until `start_work` succeeds. If `resume_work` reports a live attempt owned elsewhere, wait for a handoff or for Stash to make the item available.

## Mutate the active attempt

The following calls require the same `attempt_id` and `lease_token`, plus a stable `action_key` unique to that logical mutation:

- `checkpoint_work` records `summary`, observed `result`, and one required `next_action`, and extends the lease.
- `renew_work_lease` extends the lease when no new result exists yet.
- `submit_work_evidence` stores an `evidence_type`, `summary`, required `condition_ids`, and optional `reference` and structured `payload`.
- `verify_work_condition` marks one condition `passed` or `waived` and attaches one or more `evidence_ids`. A waiver also needs `waiver_reason`.
- `finish_work` stores the final `summary` and observed `result`, then ends a fully verified attempt.
- `handoff_work` stores the current `summary`, observed `result`, and one required `next_action`, then releases unfinished work.

Use a new action key for a different mutation. Reuse an action key only to retry the exact same request after a response may have been lost.

## Recover in a fresh session

With only a work item ID:

`resume_work` -> inspect the latest checkpoint, conditions, evidence, blockers, and attempt -> stop if a lease is live -> `prepare_work` only if conditions are absent -> `start_work` -> continue the recorded next action -> checkpoint and verify new observations -> `finish_work` or `handoff_work`.

Keep the same work item. Missing chat history or an unavailable old lease token is not a reason to create a replacement item.
