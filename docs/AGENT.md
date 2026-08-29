# Stash Work Plan Convention

Use Stash as the living, owner-facing plan. Git remains the source for code and diffs. Load the `stash-work-plan` skill before changing plan state when it is available.

## Resolve the workspace first

1. Collect local facts with `stash workspace facts --cwd . --agent-id <agent>`. A hook may provide the same fields.
2. Call `resolve_workspace` with those facts. Supply `project_namespace` only for the first binding or an explicit owner choice. Never infer it from a folder or worktree name.
3. Call `resume_workspace` with the returned namespace and worktree ID before creating or changing work. Use its default `brief` view first.
4. Continue an existing matching item. Create new work only after the project snapshot and any required paginated search show no match.

Paths, remote URLs, provider IDs, and agent IDs are identity hints. They never replace MCP authentication or grant namespace access.

## Follow the shared goal tree

- Read the shared root and `goal_context.path` from every workspace or work resume response. The path runs from the project outcome to the specific outcome this item contributes to.
- Model A-1, A-2, and deeper outcomes as child goals under A. Attach each component and task to the narrowest matching goal with `goal_id`.
- Never start detached work. When a project root exists, Stash binds legacy unassigned work to the root and rejects work assigned outside that tree.
- Link only durable context, constraints, decisions, failures, evidence, and results to goals or work. Routine narration stays out of memory.
- `finish_work` rolls verified leaf work into its goal and completes eligible parent goals. Do not mark a parent complete while child goals or executable work remain unfinished.

## Keep agent input small

- The default `resume_workspace` and `resume_work` responses are briefs. Request `detail: full` only when a referenced plan, event, evidence record, or worktree detail is necessary for the next action.
- Save `context_digest`. Send it back as `known_context_digest`; an unchanged project or work item returns a small receipt instead of repeating the same context.
- Read only the current goal path, current action, pending conditions, relevant memory, and blockers. Use IDs to fetch one missing record rather than loading every list.
- `get_goal_map` is for owner monitoring. Routine worker turns must not load the full map when a resume brief already contains their goal path.

## Claim work atomically

- Call `prepare_work` only when observable completion conditions are missing or intentionally changed.
- Call `claim_workspace` immediately before implementation with the work item ID, the same Git facts, agent ID, and a fresh random UUIDv4 action key.
- Keep the returned lease token private. Never store it or the action key in a checkpoint, memory, evidence, event, comment, or log.
- Do not replace `claim_workspace` with a manual register, attach, and start sequence. Use `start_work` only when no Git workspace is involved.
- If another live attempt owns the item or worktree, stop and follow the server response. Do not bypass it or create replacement work.

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
- Call `resolve_workspace` again after a long pause or worktree move to refresh its heartbeat.
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
