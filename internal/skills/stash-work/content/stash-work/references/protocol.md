# Project and work execution protocol

Tool names may carry a client-specific MCP prefix. Match them by the final Stash tool name.

## Start or resume a session

1. If an exact work item ID is supplied, skip the project lookup. Otherwise `resume_project(namespace, agent_id?, capabilities?, known_context_digest?)` returns the shared goal, this agent's active work, at most three runnable candidates, project counts, one next action, and a digest.
2. Continue active work first. Otherwise choose one candidate whose required capabilities are available.
3. `resume_work(work_item_id, detail="brief", known_context_digest?)` returns the focused goal path, parent plan component, owned scopes, next action, pending conditions, relevant memory, linked resource summaries, final prerequisite results, and blockers.
4. Search or create work only when these bounded responses do not already contain the intended outcome.

No local path, Git repository, or MCP Roots are required. Store each returned `context_digest`. A later call with the same value returns an unchanged receipt when the relevant view is identical.

`get_goal_map` is an owner-facing overview. Worker agents should not load it during routine turns.

The selected top-level goal is shared by every agent. Child goals divide that outcome into smaller results. Components and executable tasks carry `goal_id`; attempt start is rejected when that goal is outside the selected tree. Every work resume includes a compact `plan_context` for the current component, and every claim response repeats the compact goal path.

Capabilities, URLs, paths, provider IDs, and agent IDs never grant access. The authenticated MCP principal determines which namespace can be read or changed.

## Prepare and claim

1. `prepare_work(work_item_id, next_action, conditions, action_key)` replaces completion conditions and stores the first action. Use it only when conditions are absent or intentionally changed.
2. `claim_work(work_item_id, agent_id, action_key, lease_seconds?, worktree_id?)` creates the attempt and exclusive lease. `worktree_id` is optional Git connector metadata.

The claim action key contains a fresh random UUIDv4. An exact retry returns the same attempt with a fresh valid token. A work item can have only one live attempt.

`start_work` remains a compatibility name for `claim_work`. New integrations use `claim_work`.

## Split work under an active lease

`spawn_work(attempt_id, lease_token, action_key, relationship, title, next_action, conditions, capabilities?)` creates and prepares one new item in the same namespace and goal tree.

- `child` sets the new item's parent and blocks the current item.
- `prerequisite` blocks the current item without making it a structural child.
- `related` records a non-blocking relation.

The action is retry-safe. Reusing the action key with changed input is rejected.

## Link bounded resources

`attach_work_resource` upserts a stable resource key and links it to one work item. Supported kinds include `git`, `document`, `url`, `browser`, `api`, `dataset`, `device`, `ticket`, `file`, and `other`.

Use `authority: external` when Jira, Confluence, or another service remains authoritative. Store only bounded non-secret metadata, a short summary, revision, content digest, and URI. Credentials in a URI or metadata are rejected.

Read references with `list_work_resources`, `get_work_resource`, `stash://work/{id}/brief`, or `stash://work-resource/{id}`. Fetch an external body only when its reference is needed for the next action.

## Mutate the active attempt

These calls require the same `attempt_id`, private `lease_token`, and a stable action key unique to that mutation:

- `checkpoint_work` records a recoverable partial result and one next action before interruption, lease risk, or handoff; it is not a per-command log.
- `renew_work_lease` extends the lease when no new result exists.
- `submit_work_evidence` stores an observed result and its condition links.
- `verify_work_condition` explicitly accepts or waives a condition. Passed conditions normally skip this call by naming successfully proved pending IDs in `finish_work.passed_condition_ids`; waivers remain explicit.
- `spawn_work` decomposes newly discovered work.
- `remember_work` is required for a decision, correction, failure, or lesson.
- `finish_work` accepts the pending condition IDs explicitly named in `passed_condition_ids` only when current-attempt evidence is linked, stores the final result, verifies its memory link, and completes the work.
- `handoff_work` stores the current result and next action, verifies its memory link, then releases unfinished work.

Treat either terminal call as accepted only when its response contains `result_memory_linked: true`.

## Recover in a fresh session

When the item ID is known, start with `resume_work`; otherwise use `resume_project` once to select the item. Then stop if another live lease exists → `prepare_work` only if needed → `claim_work` → submit batched evidence → `finish_work` or `handoff_work`. Checkpoint only when the result must survive an interruption.

Keep the same project and work item. Missing chat history or an unavailable old token is not a reason to create replacements.

## Optional Git connector

For a code checkout, `stash workspace facts` can collect stable Git identity. `resolve_workspace`, `resume_workspace`, and `claim_workspace` attach that identity to the same project and attempt. They are optional connector helpers, not the project entry point.
