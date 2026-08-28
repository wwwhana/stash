---
name: stash-work
description: Resume and execute tracked work through a Stash MCP server with exclusive leases, checkpoints, evidence, completion checks, and handoffs. Use when an agent must continue or complete an existing Stash work item without duplicating work.
license: Apache-2.0
compatibility: Requires a connected Stash MCP server that exposes the work execution tools described here.
metadata:
  author: Stash
  version: "1.0.0"
---

# Stash Work

Use Stash as the durable record for a tracked work item. This skill supplies instructions only. It does not grant tool access, filesystem access, or permission to execute commands.

## Start from the existing item

1. Use the exact `work_item_id` when one is supplied. Otherwise search with `list_work_items` before creating anything.
2. Call `resume_work` before acting. Read the latest checkpoint, its single next action, completion conditions, evidence, blockers, links, and current attempt.
3. If another attempt still has a live lease, stop. Do not copy its token, change status to evade it, or create a duplicate item.
4. Call `prepare_work` only when observable completion conditions are missing or the user explicitly changed them.
5. Call `start_work` immediately before beginning. Keep the returned `lease_token` private and use it only with that attempt's mutation calls.

Use a new stable `action_key` for each logical mutation. Reuse it only when retrying the same call after an uncertain response. The key used by `start_work` is a recovery credential: generate a fresh random UUIDv4, keep it private, and never put it in a checkpoint, event, memory, or log. An exact replay returns the same attempt with a fresh valid token; every returned token remains valid only until that attempt is handed off, completed, or expires.

## Preserve observed progress

- After each meaningful action, call `checkpoint_work` with a short summary, the result actually observed, and exactly one concrete `next_action`.
- Before a long action could outlast the lease, call `renew_work_lease`. A renewal does not replace a checkpoint when a new result exists.
- Never put the lease token in checkpoints, evidence, memories, logs, or handoff text.
- Use `remember_work` only for durable decisions, corrections, failure lessons, or outcome facts. It does not prove a completion condition.

See [the call sequence](references/protocol.md) for the role of each work tool.

## Prove the result

1. Exercise each completion condition through its named path. Treat source inspection, tests, builds, HTTP behavior, UI behavior, devices, and deployed behavior as separate observations when the condition distinguishes them.
2. Call `submit_work_evidence` for the result you observed and retain the returned evidence ID.
3. Call `verify_work_condition` with that evidence ID. Use `waived` only with an explicit reason and supporting evidence.
4. Re-run `resume_work` if the accepted condition state or blockers are unclear.
5. Call `finish_work` only after every required condition has accepted evidence and every blocker is finished.

See [evidence guidance](references/evidence.md) before claiming a condition passed.

## Stop safely

If the item remains unfinished, call `handoff_work` with the current result and one concrete next action. A chat summary, comment, status edit, or terminal exit does not release the lease.

Treat the server response as authoritative for whether `start_work`, `verify_work_condition`, `finish_work`, or `handoff_work` was accepted. When a call is rejected, keep the item unfinished and follow the returned reason.
