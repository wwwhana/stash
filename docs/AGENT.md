# Stash Work Plan Convention

Maintain the Stash work plan as the living plan. The project owner reads the Plan view, not an agent's private checklist, so write it for the owner. Stash is the source of current plan state; Git remains the source of code and diffs.

If the `stash-work-plan` skill is installed, load it before changing plan state. It contains the MCP workflow and the same convention described here.

## Component map

- Plan nodes are **components of the system being built**, not phases, sprints, or a chronological to-do list. Create them with `create_plan_component`.
- Keep the map at **5 to 9 components** regardless of repository size. Grow child tasks instead of adding cards. Split a component only when one agent could no longer own it for a session.
- A component's `issue_key` is its stable identifier. Do not reuse it for a different component. Add or remove nodes instead of changing their identity.
- Use `update_plan_component` to clarify a component's wording, completion condition, implementation note, or owned paths without changing its stable identity.
- Use `delete_plan_component` or `delete_plan_task` to remove an obsolete node. Do not recycle it by changing its purpose.
- Titles are verb-led, plain-language, and concrete enough for the owner to recognize completion. Prefer `Read alerts out loud` over `Audio pipeline`, and never use vague labels such as `Decide what matters`.
- Put implementation wording in `technical_details`, not in the title. Put the owner-facing scope and done condition in `description`.

## Tasks and live state

- Create executable child tasks with `create_plan_task` and its `component_id`. A component is the map; its tasks are the work.
- Use `update_plan_task` when a task's outcome, completion condition, implementation note, or provenance needs clarification. It keeps the component, issue key, state, agents, and worktree links intact.
- Map task state as follows: `ready` is not started, `doing` is in progress, `done` is complete, and `blocked` is stuck. Use `review` only when the owner explicitly needs a review state.
- Call `start_plan_task` **before** implementation. It records `doing`, the task's `owner`, and immutable `started_by` data immediately.
- Call `complete_plan_task` the moment the task is complete. It retains who started and completed the task. Call `block_plan_task` as soon as it is stuck, and `unblock_plan_task` when the blocker is removed.
- Never batch plan updates at the end of a session. Every state change is an API write when it happens.
- For an open task, set `provenance` to `agent` when an agent is declaring its imminent build intent, or `roadmap` when the task comes from durable planning material. Leave it empty only when neither is true.

## Dependencies, paths, and worktrees

- Connect components with `link_plan_components`. `needs` means the component must come after the related component; `links` means the two components are connected. The underlying `blocks` edge remains directed.
- Put repository paths and glob patterns owned by a component in `owned_paths`. Keep them current with `set_plan_component_paths`; this tells the owner which component an agent is actually changing, including follow-up work on a completed component.
- Sync local worktrees before work starts:

```bash
stash worktree sync --repo . --namespace /projects/myapp --agent-id my-agent
```

Attach the returned worktree to the active task with `attach_worktree_to_item`. Sync again when work ends or a worktree disappears.

## Decisions and evidence

- Record a plan-affecting decision with `record_plan_decision` **before** implementing it. Attach it to the affected component or task when possible.
- Attach evidence and lessons with `link_work_item_memory`; inspect it with `list_work_item_memory_links`.
- Use `add_work_item_comment` for handoff details, reproduction notes, and decisions that do not change the plan itself.

## Semantic review

- `get_work_plan` returns deterministic convention warnings without calling a model. Resolve those warnings directly.
- Call `validate_work_plan` after meaningful component, task, dependency, or decision changes and before handing off the plan. It uses the configured Reasoner model, not the embedding model.
- Treat model findings as review advice. Fix supported component findings with `update_plan_component` and task findings with `update_plan_task`, then run `validate_work_plan` again.
- If `get_work_plan.validation.stale` is true, the saved review predates the current plan. Run it again before relying on the result.
- A plan review does not replace builds, tests, or observing the product.

## Session start and end

1. Call `init` if the Stash namespace has not been initialized.
2. Verify the exact project namespace exists, then call `recall` for it.
3. Call `get_work_plan`, `list_work_items`, and `list_worktrees` before making a new plan card. Resume the existing graph instead of rebuilding a checklist.
4. Run `validate_work_plan` when the plan meaning changed or its saved validation is stale.
5. Before ending, update each touched task, record handoff details, and save durable facts with `remember` or short-lived focus with `set_context`.

Use `create_work_item` for ad-hoc local bugs, questions, and tracker entries that do not belong in the component map. Use the plan API for the owner-facing plan; do not maintain a separate live `PLAN.md` alongside it.
