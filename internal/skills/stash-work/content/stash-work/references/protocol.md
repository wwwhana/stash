# Workspace and work execution protocol

Tool names may carry a client-specific MCP prefix. Match them by the final Stash tool name.

## Collect local Git facts

Run:

```bash
stash workspace facts --cwd . --agent-id codex
```

The command does not open Stash's database. On first use it creates `stash.repositoryInstanceId` in the repository's local Git config. It reports:

- `cwd`
- `repository_instance_id`
- `git_common_dir`
- `git_dir`
- `worktree_path`
- `remote_url`
- `repository_provider` and optional provider ID
- branch, head, worktree status, agent ID, and an optional configured project namespace

The repository instance ID separates clones. The server combines it with Git's stable worktree entry, so moving a checkout or worktree path does not create a duplicate.

## Start or resume a session

1. `resolve_workspace(local facts, project_namespace?)` binds the first checkout or resolves an existing binding, refreshes its heartbeat, and returns namespace, worktree, active item and attempt, latest checkpoint, and next action. Omitting `project_namespace` for an unbound checkout returns an explicit binding error.
2. `resume_workspace(namespace, worktree_id?, detail="brief", known_context_digest?)` returns the shared goal, current continuation, short work selection, and a digest. Use `detail="full"` only for a specific missing project detail. When more than one agent has active work, it lists the work but does not choose one arbitrary item as the current item.
3. Search or create work only when the snapshot does not already contain the requested outcome.

Store the returned `context_digest`. A later call with the same value in `known_context_digest` returns an unchanged receipt when the relevant state is identical. `resume_work` follows the same brief-first rule and includes only the current goal path, next action, pending conditions, relevant memory, and blockers by default.

`get_goal_map` is an owner-facing overview. Worker agents should not load it during routine turns when the compact resume response already contains their goal path.

The selected top-level goal is shared by every agent. Child goals divide that outcome into smaller results. Components and executable tasks carry `goal_id`; attempt start is rejected when that goal is outside the selected tree. The start or claim response repeats the compact goal path so an agent cannot begin without seeing what its work contributes to.

Remote URL, paths, provider ID, and agent ID never grant access. The authenticated MCP principal determines which namespace bindings can be read or changed.

## Prepare and claim

1. `prepare_work(work_item_id, next_action, conditions, action_key)` replaces completion conditions and stores the first action. Use it only when conditions are absent or intentionally changed.
2. `claim_workspace(work_item_id, local facts, agent_id, action_key, lease_seconds?)` performs workspace upsert, item attachment, attempt creation, and lease creation in one transaction.

The claim action key must contain a fresh random UUIDv4. An exact retry returns the same attempt with a fresh valid token. A work item can have only one live attempt, and one worktree cannot hold live attempts for two items.

Use `start_work` only for tracked work that has no Git workspace.

## Mutate the active attempt

These calls require the same `attempt_id`, private `lease_token`, and a stable action key unique to that mutation:

- `checkpoint_work` records summary, observed result, and one next action, then extends the lease.
- `renew_work_lease` extends the lease when no new result exists.
- `submit_work_evidence` stores an observed result and its condition links.
- `verify_work_condition` accepts a passed or evidence-backed waived condition.
- `finish_work` stores the final result and completes verified work.
- `handoff_work` stores the current result and next action, then releases unfinished work.

## Recover in a fresh session

`workspace facts` → `resolve_workspace` → `resume_workspace` → inspect active work and the one next action → stop if another live lease exists → `prepare_work` only if needed → `claim_workspace` → checkpoint and verify observations → `finish_work` or `handoff_work`.

Keep the same project and work item. Missing chat history or an unavailable old token is not a reason to create replacements.
