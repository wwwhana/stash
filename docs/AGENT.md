# Stash Work Plan Convention

Use Stash as the living, owner-facing AI work plan. Human work may remain authoritative in Jira, Confluence, or another external system. Git and every external connector are optional. Load the `stash-work-plan` skill before changing plan state when it is available.

## Resume the project first

1. Call `resume_project` with the exact project namespace, a stable `agent_id`, and the small set of capabilities available in this session.
2. Continue this agent's active work first. Otherwise choose one of the returned runnable items whose `required_capabilities` it can satisfy.
3. Call `resume_work` for that one item before acting. Continue an existing matching item instead of creating a replacement.
4. Call `claim_work` immediately before the first external or local action.

Namespace paths, remote URLs, provider IDs, capabilities, and agent IDs are routing hints. They never replace MCP authentication or grant namespace access.

## Follow the shared goal tree

- Read the shared root from `resume_project` and `goal_context.path` from `resume_work`. The path runs from the project outcome to the specific outcome this item contributes to.
- Model A-1, A-2, and deeper outcomes as child goals under A. Attach each component and task to the narrowest matching goal with `goal_id`.
- Never start detached work. When a project root exists, Stash binds legacy unassigned work to the root and rejects work assigned outside that tree.
- Link only durable context, constraints, decisions, failures, evidence, and results to goals or work. Routine narration stays out of memory.
- `finish_work` rolls verified leaf work into its goal and completes eligible parent goals. Do not mark a parent complete while child goals or executable work remain unfinished.

## Keep agent input small

- `resume_project` returns at most three active items and three runnable candidates. The default `resume_work` response is a brief. Request `detail: full` only when a referenced plan, event, evidence record, or connector detail is necessary for the next action.
- Save `context_digest`. Send it back as `known_context_digest`; an unchanged project or work item returns a small receipt instead of repeating the same context.
- Read only the current goal path, current action, pending conditions, relevant memory, and blockers. Use IDs to fetch one missing record rather than loading every list.
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
- Keep 5 to 9 components for the project. Grow child tasks. Split a component only when one agent could no longer own it for a working session.
- Keep component issue keys stable. Remove and add a component instead of changing its identity.
- Use verb-led, concrete titles whose completion is visible to the owner. Put implementation language in `technical_details` and the done condition in `description`.
- Keep `owned_paths` current.
- `needs` imposes order between components. `links` records interaction without order.

## Update state as work happens

- Create executable work as child tasks and set the narrowest matching `goal_id`. Use `provenance: agent` for imminent agent work and `provenance: roadmap` for durable planned work.
- Record a plan-changing decision before implementing it.
- Call `checkpoint_work` after each meaningful action with the observed result and exactly one concrete `next_action`.
- Call `renew_work_lease` before a long action could cross the lease deadline.
- Call `resume_project` again when project state may have changed. Send the prior `context_digest` so unchanged state returns a small receipt.
- Do not batch plan updates at session end.

## Prove and finish

1. Exercise each completion condition through its named path. Keep source review, builds, tests, HTTP, UI, devices, and deployment as separate observations when required.
2. Submit the observed result with `submit_work_evidence`.
3. Accept the condition with `verify_work_condition` and that evidence. Use a waiver only with an explicit reason and supporting evidence.
4. Call `finish_work` only after every required condition is accepted and all blockers are finished.
5. If unfinished, call `handoff_work` with the current result and exactly one next action. A chat summary, status edit, or heartbeat does not release the lease.

## Review plan meaning

Resolve deterministic warnings from `get_work_plan` first. Run `validate_work_plan` after meaningful plan changes and before plan handoff. It uses the reasoning model and provides review advice; it does not replace building, testing, or observing the product.

If a response contains `has_more: true`, fetch the next chunk with `offset: next_offset` and process chunks separately. Never combine every page into one model prompt.
