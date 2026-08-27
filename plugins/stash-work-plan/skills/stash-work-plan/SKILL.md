---
name: stash-work-plan
description: Manage a component-oriented living project plan in a configured Stash MCP server, including tasks, decisions, dependencies, worktrees, local issues, and Reasoner validation. Use when replacing PLAN.md, resuming or updating a Stash plan, coordinating agent work, or keeping project work state in Stash. Do not use for ordinary planning when no Stash MCP server is available.
---

# Stash Work Plan

Use the Stash MCP server as the shared, owner-facing plan. Git remains the source for code and diffs. A private agent checklist may help execute the current turn, but it does not replace Stash plan state.

## Connect to the existing plan

1. Identify the exact project namespace. Do not write to `/` when the project already uses a more specific namespace.
2. Call `init` only when Stash has not been initialized. Confirm the namespace, then use `recall` for relevant durable context.
3. Call `get_work_plan`, `list_work_items`, and `list_worktrees` before creating anything. Resume existing components and tasks instead of rebuilding them.
4. If an existing `PLAN.md` is being replaced, import its durable components, open tasks, dependencies, and decisions once. Do not delete or rewrite the file unless the user asks.

Tool names may carry a client-specific MCP prefix. Match by the final Stash tool name shown here.

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
- Call `start_plan_task` before implementation. Call `complete_plan_task` immediately when its outcome is achieved.
- Call `block_plan_task` as soon as work is stuck and `unblock_plan_task` after the blocker is removed. Do not batch state changes at session end.
- Record a plan-changing choice with `record_plan_decision` before implementing it.
- Use `create_work_item` for local bugs, questions, and ad-hoc tracker entries that do not belong in the component map.
- Put handoff notes on the relevant item with `add_work_item_comment`; attach durable evidence with `link_work_item_memory`.

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

Before ending, update every touched task, record needed handoff details, sync the worktree, save durable facts with `remember`, and return the current `get_work_plan` state to the owner.
