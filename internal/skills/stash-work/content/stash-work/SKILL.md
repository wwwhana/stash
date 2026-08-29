---
name: stash-work
description: Resume a shared Stash project and execute bounded work with goals, exclusive leases, child work, linked resources, evidence, and handoffs. Use when an agent must continue or complete Stash work without duplicating an item; Git is optional.
license: Apache-2.0
metadata:
  author: Stash
  version: "2.0.0"
---

# Stash Work

Use Stash as the durable record for AI project work. Human work may remain authoritative in an external system. Git and connector access are optional, and this skill grants no tool, filesystem, or external-service permission.

## Resume the project

1. Call `resume_project` with the exact project namespace, a stable `agent_id`, and the small set of capabilities available in this session.
2. Continue this agent's active item first. Otherwise choose one of the returned runnable items whose `required_capabilities` it can satisfy.
3. Call `resume_work` for that item. Read the shared goal path, parent plan component, owned scopes, current action, pending conditions, relevant memory, resource summaries, prerequisite results, and blockers.
4. Continue the same item instead of creating a replacement. Search only when the bounded project response does not identify the intended work.

Keep `context_digest` and send it as `known_context_digest` on later resume calls. An unchanged view returns a small receipt. See [the protocol](references/protocol.md) for recovery and connector details.

## Stay on the shared goal path

- Treat the selected top-level goal as the project outcome. A-1, A-2, and deeper results belong under it.
- Follow `plan_context`: it gives this item only its parent component, component outcome, task details, and owned scopes instead of loading the full plan.
- Give new components and work items the narrowest matching `goal_id`. Stash rejects attempt starts outside the selected tree and binds older unassigned work to the shared root.
- Reserve `get_goal_map` for owner monitoring. Worker turns use the short project and work resumes.
- Save only durable constraints, decisions, failures, and results as memory. Do not store routine narration.

## Claim one item

1. Call `prepare_work` only when observable completion conditions are missing or the owner intentionally changed them.
2. Call `claim_work` immediately before acting, using the work item ID, agent ID, and a fresh random UUIDv4 `action_key`.
3. Keep the returned `lease_token` private. If another agent holds the item, stop and follow the server response.

Use a new action key for each logical mutation. Reuse it only to retry the exact same request after an uncertain response. Never store an action key or lease token in memory, checkpoints, evidence, events, comments, or logs.

## Split discovered work

- Call `spawn_work` during the active attempt when another child, prerequisite, or related result is required.
- Give it one concrete first action, observable completion conditions, and only the capabilities it needs.
- Child and prerequisite work block the parent. Let the assigned agent finish that item and reuse its recorded final result.

## Keep input small

- Use `attach_work_resource` for Jira, Confluence, Git, documents, browser targets, APIs, datasets, devices, and artifacts.
- Store only a stable key, short summary, revision, and URI. External document bodies and credentials stay in their original systems.
- Fetch one linked resource only when the current `next_action` needs it.
- Use `detail: full` only for a specific missing record. Do not load the full Goal Map into a worker turn.

## Preserve observed progress

- Call `checkpoint_work` after every meaningful action with a short summary, the observed result, and exactly one concrete `next_action`.
- Call `renew_work_lease` before a long action could cross the lease deadline.
- Use `remember_work` for durable decisions, corrections, failure lessons, and outcome facts. It does not prove completion.
- Call `resume_project` again when project state may have changed.

## Prove and finish the result

1. Exercise each completion condition through its named path. Keep source review, tests, builds, HTTP, UI, devices, and deployment as separate observations when the condition distinguishes them.
2. Call `submit_work_evidence` for what was observed and retain its evidence ID.
3. Call `verify_work_condition` with that evidence ID. Use `waived` only with an explicit reason and supporting evidence.
4. Call `finish_work` only after every required condition has accepted evidence and every blocker is finished.

Read [evidence guidance](references/evidence.md) before claiming a condition passed.

## Stop safely

Call `handoff_work` before ending unfinished work. Save the current result and exactly one next action. A chat summary, comment, status edit, terminal exit, or connector heartbeat does not release the lease.

Treat the Stash response as authoritative. If a claim, verification, finish, or handoff is rejected, the item remains unfinished.
