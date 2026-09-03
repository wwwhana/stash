# Stash Work Plan Convention

Use Stash as the living, owner-facing AI work plan. Human work may remain authoritative in Jira, Confluence, or another external system. Git and every external connector are optional. Load the `stash-work-plan` skill before changing plan state when it is available.

## Resume the project first

1. Use this workflow only for an existing Stash item or a user-requested shared Work Plan. Do not call Stash merely because a session started.
2. If an exact work item ID is supplied, skip `resume_project`. Otherwise call it once with the exact project namespace, a stable `agent_id`, and the small set of capabilities available in this session.
3. Continue this agent's active work first. Otherwise choose one returned runnable item whose `required_capabilities` it can satisfy.
4. Call `resume_work` once for that item, then continue the existing item instead of creating a replacement.
5. Call `claim_work` immediately before the first external or local action. Resume again only after a conflict, stale-state response, handoff, or explicit refresh request.

Namespace paths, remote URLs, provider IDs, capabilities, and agent IDs are routing hints. They never replace MCP authentication or grant namespace access.

## Follow the shared goal tree

- Read the shared root from `resume_project`, then read `goal_context.path` and `plan_context` from `resume_work`. The goal path identifies the result this item contributes to; the plan context identifies its parent component, component outcome, task details, and owned scopes.
- Model A-1, A-2, and deeper outcomes as child goals under A. Attach each component and task to the narrowest matching goal with `goal_id`.
- Never start detached work. When a project root exists, Stash binds legacy unassigned work to the root and rejects work assigned outside that tree.
- Link only durable context, constraints, decisions, failures, evidence, and results to goals or work. Routine narration stays out of memory.
- `finish_work` rolls verified leaf work into its goal and completes eligible parent goals. Do not mark a parent complete while child goals or executable work remain unfinished.

## Keep agent input small

- `resume_project` returns at most three active items and three runnable candidates. The default `resume_work` response is a brief. Request `detail: full` only when a referenced plan, event, evidence record, or connector detail is necessary for the next action.
- Save `context_digest`. Send it back as `known_context_digest`; an unchanged project or work item returns a small receipt instead of repeating the same context.
- When `context_window.next_query` is present, copy its digest and `fact_offset` fields into the next `resume_work` call. Continue until the response points at its own current digest with offset zero. If the target changed between pages, restart from the returned reset cursor.
- Treat `changed_facts` as a delta: it contains only facts added, updated, or removed since the saved digest. `evidence_references` and resource summaries are pointers; fetch one payload only when the next action requires it.
- Keep `context_window.input_bytes` within `input_limit_bytes`. A truncated brief tells you how to continue or when to request `detail: full`; never combine every page into one model prompt.
- Read only the current goal path, parent plan component, owned scopes, current action, pending conditions, relevant non-fact memory, changed facts, and blockers. Use IDs to fetch one missing record rather than loading every list.
- `get_goal_map` is for owner monitoring. Routine worker turns must not load the full map when a resume brief already contains their goal path.

## Claim work atomically

- Call `prepare_work` only when observable completion conditions are missing or intentionally changed.
- Call `claim_work` immediately before acting with the work item ID, agent ID, and a fresh random UUIDv4 action key. A `worktree_id` is optional connector metadata.
- Keep the returned lease token private. Never store it or the action key in a checkpoint, memory, evidence, event, comment, or log.
- If another live attempt owns the item, stop and follow the server response. Do not bypass it or create replacement work.

## Decompose work during execution

- Call `spawn_work` when the active item reveals a child, prerequisite, or related result that another agent can own.
- Give the new item one concrete first action, observable completion conditions, and only the capabilities it actually requires.
- Child and prerequisite items block the current item until they finish. Do not finish the parent by copying a child's expected result into a checkpoint.
- Use `set_work_capabilities` to correct routing hints. Capabilities do not grant tool or namespace access.

## Link only the material needed next

- Use `attach_work_resource` for Jira issues, Confluence pages, Git checkouts, documents, browser targets, APIs, datasets, devices, and result artifacts.
- Keep external document bodies and connector credentials in their original systems. Store a stable key, short summary, revision, and URI.
- Read only the resource needed for the current `next_action`. The `stash://work/{id}/brief` and `stash://work-resource/{id}` resources expose bounded MCP views.
- Treat `authority: external` as a clear ownership boundary: Stash records the AI work and reference; it does not silently become the source of human status.

## Maintain the component map

- Components are parts of the system, not phases, sprints, milestones, or chronological buckets.
- Create one component for each independently owned part of the system. Put executable work in child tasks.
- Keep component issue keys stable. Remove and add a component instead of changing its identity.
- Use verb-led, concrete titles whose completion is visible to the owner. Put implementation language in `technical_details` and the done condition in `description`.
- Keep each component's owned scopes current. A scope may be a file pattern, connector resource, external system, or other work boundary.
- `needs` imposes order between components. `links` records interaction without order.

## Update state as work happens

- Create executable work as child tasks and set the narrowest matching `goal_id`. Use `provenance: agent` for imminent agent work and `provenance: roadmap` for durable planned work.
- Record a plan-changing decision before implementing it.
- Do not call Stash after routine shell commands, file reads, or edits.
- Call `checkpoint_work` only before interruption, lease risk, or when a partial result must survive for another agent. Include exactly one concrete `next_action`.
- Combine related durable details into one `remember_work` call for a distinct decision, correction, failure, or lesson. Final results are saved by `finish_work` or `handoff_work`.
- Call `renew_work_lease` before a long action could cross the lease deadline.
- Resume again only when Stash reports a conflict or stale state. Send the prior `context_digest` when refreshing.
- Do not batch plan updates at session end.

## Prove and finish

1. Exercise each completion condition through its named path. Keep source review, builds, tests, HTTP, UI, devices, and deployment as separate observations when required.
2. Use one `submit_work_evidence` call for every condition proved by the same observation.
3. Put the successfully proved pending IDs in `finish_work.passed_condition_ids`; it accepts them only when this attempt supplied linked evidence. Use `verify_work_condition` only for an explicit waiver or when acceptance must be recorded before finish.
4. Call `finish_work` only after every required condition has evidence and all blockers are finished.
5. Confirm `result_memory_linked: true` in a successful `finish_work` response.
6. If unfinished, call `handoff_work` with the current result and exactly one next action, then confirm `result_memory_linked: true`. A chat summary, status edit, or heartbeat does not release the lease.

## Review plan meaning

Resolve deterministic warnings from `get_work_plan` first. Run `validate_work_plan` after meaningful plan changes and before plan handoff. It uses the reasoning model and provides review advice; it does not replace building, testing, or observing the product.

If a response contains `has_more: true`, fetch the next chunk with `offset: next_offset` and process chunks separately. Never combine every page into one model prompt.
