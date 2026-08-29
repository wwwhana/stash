---
name: stash-work
description: Resolve a Git workspace, resume its Stash project, and execute tracked work with exclusive leases, checkpoints, evidence, and handoffs. Use when an agent must continue or complete Stash work without guessing a namespace or duplicating an item.
license: Apache-2.0
metadata:
  author: Stash
  version: "1.1.0"
---

# Stash Work

Use Stash as the durable record for project work. Git remains the source for code and diffs. This skill grants no tool or filesystem permissions.

## Resolve the current workspace

1. Collect local facts with `stash workspace facts --cwd . --agent-id <agent>`. Hooks may collect the same fields. Treat the output as identity hints, never as authorization.
2. Call `resolve_workspace` with those facts. Supply `project_namespace` only for the first binding or when the owner explicitly chose it. Never derive a namespace from a folder name.
3. Call `resume_workspace` with the returned namespace and worktree ID. Start with its default brief and read the shared goal, current continuation, short work selection, and `next_action` before creating anything.
4. Search only if the workspace snapshot does not identify the intended item. Continue an existing matching item instead of creating a replacement.

`resolve_workspace` refreshes the worktree heartbeat and detects path moves through its stable Git identity. See [the protocol](references/protocol.md) for required fields and recovery behavior.

## Stay on the shared goal path

- Read `goal_context.path` before acting. It contains the shared project outcome and the narrower child outcome for the current work.
- Give new components and tasks the narrowest matching `goal_id`. Stash rejects attempt starts outside the selected goal tree and binds older unassigned work to the shared root.
- Keep `context_digest` and return it as `known_context_digest` on later resume calls. An unchanged context returns a small receipt.
- Use `detail: full` only to fetch a specific missing plan, graph, event, evidence, or worktree detail.
- Reserve `get_goal_map` for owner monitoring. Worker turns should not load the full map when the resume brief already contains their goal path.
- Save only durable constraints, decisions, failures, and results as memory. Do not store routine narration.

## Claim tracked work atomically

1. Call `prepare_work` only when observable completion conditions are missing or the owner intentionally changed them.
2. Call `claim_workspace` immediately before implementation, using the same local facts, the work item ID, agent ID, and a fresh random UUIDv4 `action_key`.
3. Keep the returned `lease_token` private. If another item or agent holds the worktree or item, stop and follow the server response.

`claim_workspace` resolves or updates the worktree, attaches it to the item, and creates the attempt and lease in one transaction. Do not replace it with a manual `register_worktree` → `attach_worktree_to_item` → `start_work` sequence. Use `start_work` only when no Git workspace is involved.

Use a new action key for each logical mutation. Reuse it only to retry the exact same request after an uncertain response. Never store an action key or lease token in memory, checkpoints, evidence, events, or logs.

## Preserve observed progress

- Call `checkpoint_work` after every meaningful action with a short summary, the observed result, and exactly one concrete `next_action`.
- Call `renew_work_lease` before a long action could cross the lease deadline.
- Use `remember_work` for durable decisions, corrections, failure lessons, and outcome facts. It does not prove completion.
- Re-run `resolve_workspace` as a heartbeat after a long pause or worktree move. Re-run `resume_workspace` at handoff or when project state may have changed.

## Prove and finish the result

1. Exercise each completion condition through its named path. Keep source review, tests, builds, HTTP, UI, devices, and deployment as separate observations when the condition distinguishes them.
2. Call `submit_work_evidence` for what was observed and retain its evidence ID.
3. Call `verify_work_condition` with that evidence ID. Use `waived` only with an explicit reason and supporting evidence.
4. Call `finish_work` only after every required condition has accepted evidence and every blocker is finished.

Read [evidence guidance](references/evidence.md) before claiming a condition passed.

## Stop safely

Call `handoff_work` before ending unfinished work. Save the current result and exactly one next action. A chat summary, comment, status edit, terminal exit, or worktree heartbeat does not release the lease.

Treat the Stash response as authoritative. If a claim, verification, finish, or handoff is rejected, the item remains unfinished.
