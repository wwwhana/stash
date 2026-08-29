---
name: stash-work-plan
description: Manage and resume a shared, component-oriented Stash project across agents with hierarchical goals, bounded context, exclusive work claims, child work, linked resources, checkpoints, evidence, handoffs, decisions, dependencies, and plan review. Git and external connectors are optional. Use when replacing PLAN.md, coordinating agents, or continuing Stash work. Do not use when no Stash MCP server is connected.
---

# Stash Work Plan

Use Stash as the shared AI plan and continuity record. Human work may remain authoritative in Jira, Confluence, or another external system. A private checklist may guide the current turn but never replaces Stash state.

## Resume before creating

1. Call `resume_project` with the exact namespace, a stable `agent_id`, and the small set of capabilities available in this session.
2. Continue this agent's active work first. Otherwise choose one returned candidate whose `required_capabilities` it can satisfy.
3. Call `resume_work` for that item and read its compact goal path, parent plan component, owned scopes, next action, pending conditions, relevant memory, linked resource summaries, prerequisite results, and blockers.
4. Continue a matching item. Create a component, task, or issue only when the bounded responses and any needed paginated search show no match.

No local path, Git repository, or MCP Roots are required. Capabilities, paths, URLs, provider IDs, and agent IDs are routing hints; MCP authentication controls namespace access.

Keep `context_digest` and send it as `known_context_digest` on the next resume. When nothing relevant changed, Stash returns a small unchanged receipt. Large list responses may contain `has_more: true`; fetch `next_offset` and process chunks separately.

Reserve `get_goal_map` for owner monitoring. Worker turns use `resume_project` and `resume_work` unless one specific missing record is required.

## Follow the shared outcome tree

- Treat the selected top-level goal as the project outcome. Child goals are A-1, A-2, and deeper results that combine into it.
- Read `goal_context.path` and `plan_context` before acting. The plan context gives the current component and owned scopes without loading the full plan. Attach each component and task to the narrowest matching goal with `goal_id`.
- Do not work around a missing or rejected goal link.
- Link durable context or results with `link_goal_memory` or `remember_work`; omit routine narration.
- Verified leaf work completes eligible child and parent goals. Never complete a parent while child work remains unfinished.

## Maintain the component map

- Components are recognizable parts of the system, not phases, sprints, milestones, or chronological buckets. Create them with `create_plan_component`.
- Create one component for each independently owned part of the system. Put executable work in child tasks, and split a component when one agent can no longer own it for a working session.
- Treat each component issue key as stable. Remove an obsolete component and add a new one instead of changing its identity.
- Use verb-led, plain-language titles whose completion is visible to the owner. Put implementation language in `technical_details` and the done condition in `description`.
- Keep owned scopes current. A scope may be a repository pattern, connector resource, external system, or another work boundary.
- Use `link_plan_components`: `needs` imposes order; `links` records interaction without order.

## Update work immediately

- Create executable child work with `create_plan_task` and the narrowest matching `goal_id`.
- Use `update_plan_task` to clarify an outcome, done condition, implementation detail, or provenance without changing identity.
- Record a plan-changing choice with `record_plan_decision` before implementing it.
- Use `create_work_item` for bugs, questions, and tracker entries outside the component map.
- Use comments and memory links for supporting context. They do not replace completion evidence.
- Do not batch state changes at session end.

## Claim one item

1. Call `prepare_work` only when required completion conditions are absent or intentionally changed.
2. Call `claim_work` immediately before acting with the work item ID, agent ID, and a fresh random UUIDv4 action key.
3. Keep the returned lease token private. Do not copy another attempt's token, evade a live lease, or create duplicate work.

A `worktree_id` is optional connector metadata. Use a new action key for every logical mutation and reuse it only for an exact retry after an uncertain response. Never store action keys or lease tokens in checkpoints, evidence, memories, events, comments, or logs.

## Split work during execution

- Call `spawn_work` when the current item reveals a child, prerequisite, or related result.
- Give the new item one concrete first action, observable completion conditions, and only the capabilities it requires.
- Child and prerequisite items block the current item until they finish. Their final recorded result is included in the parent's later brief.

## Link bounded resources

- Use `attach_work_resource` for Jira issues, Confluence pages, Git checkouts, documents, browser targets, APIs, datasets, devices, and artifacts.
- Keep external bodies and credentials in their original systems. Store only a stable key, short summary, revision, digest, and URI.
- Use `authority: external` when the external system remains the source for human work. Stash remains the source for AI execution state.
- Fetch a linked body only when its reference is needed for the current `next_action`.

## Checkpoint observed progress

- Call `checkpoint_work` after each meaningful action. Record what was observed and exactly one concrete `next_action`.
- Call `renew_work_lease` before a long action could cross the deadline.
- Use `remember_work` for durable decisions, corrections, failure lessons, and outcome facts.
- Call `resume_project` again when project state may have changed.

## Prove and finish

1. Exercise each condition through its named path. Keep source, build, test, HTTP, UI, device, and deployment observations separate when required.
2. Call `submit_work_evidence` and retain the evidence ID.
3. Call `verify_work_condition` with that ID. Use `waived` only with an explicit reason and supporting evidence.
4. Call `finish_work` only after every required condition is accepted and every blocker is finished.

If unfinished, call `handoff_work` with the current observed result and exactly one next action. A comment, chat summary, status edit, or connector heartbeat does not release a lease.

## Review plan meaning

`get_work_plan` reports deterministic convention warnings without a model call. Resolve those first.

Call `validate_work_plan` after meaningful component, task, dependency, or decision changes and before plan handoff. It uses the configured reasoning model, not the embedding model. Treat findings as review advice, fix supported findings through plan tools, and rerun validation when the saved result is stale. Plan review never replaces building, testing, or observing the product.

## Fresh-agent recovery

`resume_project` → continue the same active item or choose one candidate → `resume_work` → `prepare_work` only if needed → `claim_work` → checkpoint observations → submit and verify evidence → `finish_work`, or `handoff_work` if unfinished.

Never reconstruct project state from old chat when the bounded Stash resumes are available.

## Optional Git connector

Code projects may use `stash workspace facts`, `resolve_workspace`, `resume_workspace`, `claim_workspace`, and `stash worktree sync` to attach stable checkout identity. They are optional conveniences, not the project entry path.
