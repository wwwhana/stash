---
name: stash-work-plan
description: Manage and resume component-oriented project work in a configured Stash MCP server, including leased execution, checkpoints, evidence, handoffs, decisions, dependencies, worktrees, and Reasoner validation. Use when replacing PLAN.md, resuming or executing a Stash work item, coordinating agent work, or keeping project work state in Stash. Do not use for ordinary planning when no Stash MCP server is available.
---

# Stash Work Plan

Use the Stash MCP server as the shared, owner-facing plan. Git remains the source for code and diffs. A private agent checklist may help execute the current turn, but it does not replace Stash plan state.

## Resume before creating

1. Identify the exact project namespace. Do not write to `/` when the project already uses a more specific namespace.
2. Call the idempotent `init` at the start of the session. Confirm the namespace, then use `recall` for relevant durable context.
3. Call `get_work_plan`, `list_work_items`, and `list_worktrees` before creating anything. If a work item ID is already known, call `resume_work` for that item before any mutation.
4. Search all returned pages for an existing component or task that represents the work. Call `resume_work` on the matching item and continue it. Create a new item only when no existing item fits.
5. If an existing `PLAN.md` is being replaced, import its durable components, open tasks, dependencies, and decisions once. Do not delete or rewrite the file unless the user asks.

Tool names may carry a client-specific MCP prefix. Match by the final Stash tool name shown here.

Every leased mutation requires the `attempt_id`, private `lease_token`, and an `action_key`. Use one unique, stable action key for each logical mutation, and reuse that key only when retrying the exact same call after an uncertain response. The `start_work` action key is also a recovery credential: generate a fresh random UUIDv4, keep it private, and never reuse it for another action. An exact replay returns the same attempt with a fresh valid token; every returned token remains valid only until that attempt is handed off, completed, or expires.

Large list responses are split by the server. When a result contains `has_more: true`, call the same tool again with `offset` set to `next_offset`. Process one chunk at a time; do not combine every page into one model prompt.

## Maintain the component map

- Components are recognizable parts of the system, not phases, sprints, milestones, or chronological buckets. Create them with `create_plan_component`.
- Keep 5 to 9 components for the whole project. Add child tasks as work grows. Split a component only when one agent could no longer own it for a working session.
- Treat each component `issue_key` as a stable identity. Add or remove a component instead of reusing its key for a different purpose.
- Use `update_plan_component` to clarify wording, completion criteria, technical detail, or owned paths while preserving that identity.
- Write verb-led, plain-language titles that let the owner recognize completion. Put implementation language in `technical_details` and the visible completion condition in `description`.
- Keep `owned_paths` current with `set_plan_component_paths` whenever responsibility moves.
- Connect components with `link_plan_components`: `needs` means the component comes after the related component; `links` means they interact without imposing order.

## Update work as it happens

- Create executable child work with `create_plan_task`. Use `provenance: agent` for the caller's imminent work and `provenance: roadmap` for durable planned work.
- Use `update_plan_task` to fix an existing task's outcome, completion criteria, technical detail, or provenance without changing its component, state, agents, or worktree links.
- Use the leased execution flow below for implementation. Do not substitute `start_plan_task`, `complete_plan_task`, or a manual status update for `start_work`, `handoff_work`, or `finish_work`.
- Call `block_plan_task` as soon as a durable blocker is known and `unblock_plan_task` after it is removed. If an attempt is active, checkpoint and hand it off before ending the session. Do not batch state changes at session end.
- Record a plan-changing choice with `record_plan_decision` before implementing it.
- Use `create_work_item` for local bugs, questions, and ad-hoc tracker entries that do not belong in the component map.
- Put supporting context on the relevant item with `add_work_item_comment` or `link_work_item_memory`. These links do not replace completion evidence submitted and verified through the leased execution flow.

## Claim resumable work

1. Call `resume_work` immediately before acting. Read the item, latest attempt and checkpoint, completion conditions, evidence, blockers, linked worktrees and memories, and recent events.
2. If the server reports a live lease, do not implement, change status to evade it, reuse its token, or create a duplicate item. A fresh session cannot recover another attempt's lease token. Ask for a handoff or wait until the server itself makes the item claimable.
3. If the item has no completion conditions, call `prepare_work` with observable conditions. Mark conditions required only when they must pass for completion.
4. Call `start_work` with the current `agent_id` and a fresh random UUIDv4 `action_key`, and proceed only when it returns an attempt and lease token. Keep every returned token private and pass it only to that attempt's mutation tools.
5. Call `renew_work_lease` before a long action could cross the lease deadline. A local clock or an expired-looking timestamp does not authorize bypassing a lease; the server decides whether the item is claimable.

## Checkpoint observed progress

- Call `checkpoint_work` after every meaningful action. Set `result` to what was actually observed, not what the code suggests or what should happen.
- Set `next_action` to exactly one concrete action. Do not put a checklist, alternatives, or multiple actions joined together in that field.
- Keep `summary` short enough for the next agent to scan. A lease renewal does not replace a checkpoint.
- Use `remember_work` for typed durable decisions, facts, failures, and handoff context discovered while executing the item. Do not store routine narration or secrets.

## Prove and finish the outcome

1. Exercise the completion path named by each condition. Treat source inspection, builds, tests, UI observation, device behavior, and deployed behavior as separate evidence when the condition distinguishes them.
2. Call `submit_work_evidence` with the observed result and a durable reference or object payload, and retain the returned evidence ID. Do not claim broader coverage than the evidence shows.
3. Call `verify_work_condition` with the condition ID, `passed` or `waived` status, and the supporting evidence IDs. This call attaches the evidence while verifying the condition; a waiver also needs a written reason. Treat only the server's accepted response as verification.
4. Re-read the item with `resume_work` when the condition state is unclear. Call `finish_work` only after the server has accepted every required condition and no unfinished blocker remains.
5. Completion means `finish_work` returned success. If it rejects the request, the item is still unfinished; follow the returned unmet conditions instead of marking the task done elsewhere.

## Hand off unfinished work

Before ending a session with unfinished work:

1. Choose exactly one concrete next action from the latest observed result.
2. Save any durable context with `remember_work` and sync the linked worktree.
3. Call `handoff_work` with the current summary, observed result, and that one next action. It stores the final checkpoint and releases the lease in one server transaction. Confirm that the server accepted the handoff.

Do not intentionally leave a live lease behind. A comment, chat summary, worktree sync, or task status change does not release it.

## Dory recovery with only an item ID

For a fresh session that knows only `work_item_id`, use this compact sequence:

`init` -> `resume_work(work_item_id)` -> inspect the latest checkpoint's one `next_action`, conditions, evidence, blockers, and linked worktree -> stop if the server reports a live lease -> `prepare_work` only if conditions are absent -> `start_work` -> continue the recorded next action -> `checkpoint_work` after observing its result -> `submit_work_evidence` -> `verify_work_condition` -> `finish_work`, or `handoff_work` with its final checkpoint if unfinished.

Use the resume bundle instead of reconstructing state from an old chat. Never create a replacement item merely because the previous agent or lease token is unavailable.

## Keep worktrees attached

Sync the current Git worktree before work starts:

```bash
stash worktree sync --repo . --namespace /projects/myapp --agent-id codex
```

Use the actual namespace and agent name. Attach the returned worktree to the active task with `attach_worktree_to_item`. Sync again before handoff and after a worktree is merged, removed, or missing.

## Check plan meaning

`get_work_plan` returns deterministic convention warnings without a model call. Resolve those first.

Call `validate_work_plan` after meaningful component, task, dependency, or decision changes and before a plan handoff. This invokes the configured Reasoner model, not the embedding model. It checks whether outcomes are concrete, tasks fit their components, component boundaries overlap, dependencies make sense, and decisions conflict.

- Treat findings as review advice, not automatic authority over the owner's intent.
- Fix supported component findings with `update_plan_component` and task findings with `update_plan_task`, then run `validate_work_plan` again.
- When `get_work_plan.validation.stale` is true, the saved review predates the current plan and must be run again before relying on it.
- Do not use semantic validation as a substitute for builds, tests, or observing the product.

Before ending, update every touched task, complete or hand off every claimed attempt, sync the worktree, save durable project facts with `remember`, and return the current `get_work_plan` state to the owner.
