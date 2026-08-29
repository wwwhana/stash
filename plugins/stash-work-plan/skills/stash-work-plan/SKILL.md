---
name: stash-work-plan
description: Manage and resume a component-oriented project plan in Stash, including automatic Git workspace resolution, atomic work claims, checkpoints, evidence, handoffs, decisions, dependencies, and semantic plan review. Use when replacing PLAN.md, working across worktrees, coordinating agents, or continuing Stash work. Do not use when no Stash MCP server is connected.
---

# Stash Work Plan

Use Stash as the shared plan and continuity record. Git remains the source for code and diffs. A private checklist may guide the current turn but never replaces Stash state.

## Resolve before creating

1. Run `stash workspace facts --cwd . --agent-id <agent>`. If a lifecycle hook already supplied those fields, reuse them.
2. Call `resolve_workspace` with the local facts. Supply `project_namespace` only for a first binding or an explicit owner choice. Do not infer it from the current folder or worktree name.
3. Call `resume_workspace` with the returned namespace and worktree ID. Start with the default brief: shared goal, current continuation, short work selection, and one next action. Request `detail: full` only for a specific missing plan or graph detail.
4. Continue a matching existing item. Create a component, task, or issue only after the workspace snapshot and any needed paginated search show no match.

The Git facts are identity hints, not authorization. The MCP authentication principal controls namespace access. A moved checkout keeps its identity because Stash keys it by repository instance and Git worktree entry rather than display path.

Large list responses can contain `has_more: true`. Fetch the next chunk with `offset: next_offset` and process chunks separately.

Keep the brief `context_digest` and send it as `known_context_digest` on the next resume call. When nothing relevant changed, Stash returns a small unchanged receipt. Do not spend model input on a repeated full snapshot.

Reserve `get_goal_map` for owner monitoring. Worker turns should rely on the compact goal path from `resume_workspace` or `resume_work` unless one specific missing record is required.

## Follow the shared outcome tree

- Treat the selected top-level goal as the project outcome. Child goals are the A-1, A-2, and deeper results that combine into it.
- Read `goal_context.path` before acting. Attach each component and task to the narrowest matching child with `goal_id`.
- A start or claim returns the same compact goal path. Do not work around a missing or rejected goal link.
- Link durable context or results with `link_goal_memory` or `remember_work`; omit routine narration.
- Verified leaf work completes eligible child and parent goals automatically. Never complete a parent while a child outcome remains unfinished.

## Maintain the component map

- Components are recognizable parts of the system, not phases, sprints, milestones, or chronological buckets. Create them with `create_plan_component`.
- Keep 5 to 9 components for the project. Grow child tasks. Split a component only when one agent could no longer own it for a working session.
- Treat each component issue key as stable. Remove an obsolete component and add a new one instead of changing its identity.
- Use verb-led, plain-language titles whose completion is visible to the owner. Put implementation language in `technical_details` and the done condition in `description`.
- Keep `owned_paths` current with `set_plan_component_paths`.
- Use `link_plan_components`: `needs` imposes order; `links` records interaction without order.

## Update work immediately

- Create executable child work with `create_plan_task` and the narrowest matching `goal_id`. Use `provenance: agent` for imminent agent work and `provenance: roadmap` for durable planned work.
- Use `update_plan_task` to clarify an outcome, done condition, implementation detail, or provenance without changing identity.
- Record a plan-changing choice with `record_plan_decision` before implementing it.
- Use `create_work_item` for bugs, questions, and local tracker entries outside the component map.
- Use comments and memory links for supporting context. They do not replace completion evidence.
- Do not batch state changes at session end.

## Claim the item and worktree together

1. Call `prepare_work` only when required completion conditions are absent or intentionally changed.
2. Call `claim_workspace` immediately before implementation with the work item ID, the same Git facts, agent ID, and a fresh random UUIDv4 action key.
3. Keep the returned lease token private. Do not copy another attempt's token, evade a live lease, or create duplicate work.

`claim_workspace` resolves or registers the worktree, attaches it, and starts the attempt in one transaction. Do not use the old manual `register_worktree` → `attach_worktree_to_item` → `start_work` chain. `start_work` remains for work without a Git workspace.

Use a new action key for every logical mutation and reuse it only for an exact retry after an uncertain response. Never store action keys or lease tokens in checkpoints, evidence, memories, events, or logs.

## Checkpoint observed progress

- Call `checkpoint_work` after each meaningful action. Record what was observed and exactly one concrete `next_action`.
- Call `renew_work_lease` before a long action could cross the deadline.
- Use `remember_work` for durable decisions, corrections, failure lessons, and outcome facts.
- Re-run `resolve_workspace` as a heartbeat after a long pause or path move. Use `stash worktree sync` when reconciling every worktree in the repository.

## Prove and finish

1. Exercise each condition through its named path. Keep source, build, test, HTTP, UI, device, and deployment observations separate when required.
2. Call `submit_work_evidence` and retain the evidence ID.
3. Call `verify_work_condition` with that ID. Use `waived` only with an explicit reason and supporting evidence.
4. Call `finish_work` only after every required condition is accepted and every blocker is finished.

If unfinished, call `handoff_work` with the current observed result and exactly one next action. A comment, chat summary, status edit, or heartbeat does not release a lease.

## Review plan meaning

`get_work_plan` reports deterministic convention warnings without a model call. Resolve those first.

Call `validate_work_plan` after meaningful component, task, dependency, or decision changes and before plan handoff. It uses the configured reasoning model, not the embedding model. Treat findings as review advice, fix supported findings through plan tools, and rerun validation when the saved result is stale. Plan review never replaces building, testing, or observing the product.

## Dory recovery

For a fresh agent with only the checkout:

`workspace facts` → `resolve_workspace` → brief `resume_workspace` → read the goal path and next action → `prepare_work` only if needed → `claim_workspace` → checkpoint observations → submit and verify evidence → `finish_work`, or `handoff_work` if unfinished.

Never reconstruct project state from old chat when the workspace resume bundle is available.
