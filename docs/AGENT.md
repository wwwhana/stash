# Stash Work Plan Convention

Maintain the Stash work plan as the living plan. The project owner reads the Plan view, not an agent's private checklist, so write it for the owner. Stash is the source of current plan state; Git remains the source of code and diffs.

If the `stash-work-plan` skill is installed, load it before changing plan state. It contains the MCP workflow and the same convention described here.

## Work execution continuity

- Before creating tracked work, search the exact project namespace with `get_work_plan` and `list_work_items`. If a matching item exists, call `resume_work` with its ID and continue it. Never create a second item because the previous session is missing from the current chat.
- Call `resume_work` before acting on an existing item. Prepare completion conditions only when they are missing or the owner deliberately changes them, then call `start_work` with a fresh random UUIDv4 action key to acquire the item. Keep every returned `lease_token` private and use it only for that attempt. An exact retry returns the same attempt with a fresh valid token; all returned tokens end together on handoff, completion, or expiry.
- A live lease belongs to one attempt. If `start_work` reports an active lease, do not bypass it, change the completion conditions, or create replacement work. Continue only after the owner hands it off or the lease expires.
- Call `checkpoint_work` after every meaningful action. Record the observed result and exactly one concrete, non-empty `next_action`; routine narration is not a checkpoint.
- Submit observable evidence with `submit_work_evidence`, link it to the relevant condition, and call `verify_work_condition`. Never mark an execution-managed item done without linked evidence for every required condition.
- Call `finish_work` only after all required conditions have passed or have an evidence-backed waiver and all blocking items are finished. For a plan task, `start_work` and `finish_work` record `started_by` and `completed_by` in the same transactions.
- Before ending an unfinished attempt, write a final checkpoint through `handoff_work`. Its `next_action` must tell the next agent exactly what to do. If the session ends unexpectedly, the next agent must call `resume_work` on the same item and wait for the live lease to expire instead of duplicating the work.

See [WORK_EXECUTION.md](WORK_EXECUTION.md) for the complete call sequence, ownership rules, recovery flow, and interruption test.

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
- Prepare the task's completion conditions, then call `start_work` **before** implementation. It records `doing`, the task's `owner`, and immutable `started_by` data immediately.
- Call `finish_work` the moment the verified task is complete. It retains who started and completed the task. If work is stuck, checkpoint the observed blocker, hand off the active attempt, and then use `block_plan_task`; call `unblock_plan_task` when the blocker is removed.
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
3. Call `get_work_plan`, `list_work_items`, and `list_worktrees` before making a new plan card. If matching work exists, call `resume_work` with that item ID; create new work only after confirming there is no match.
4. Run `validate_work_plan` when the plan meaning changed or its saved validation is stale.
5. Before ending an active attempt, either finish it with verified evidence or call `handoff_work` with one concrete next action. Save durable facts with `remember_work` or short-lived focus with `set_context`.

Use `create_work_item` for ad-hoc local bugs, questions, and tracker entries that do not belong in the component map. Use the plan API for the owner-facing plan; do not maintain a separate live `PLAN.md` alongside it.
