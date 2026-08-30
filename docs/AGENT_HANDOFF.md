# Project Resume and Handoff

Stash gives each Web MCP agent a bounded view of the shared project and a durable place to leave the next action. The workflow works for code, documents, research, browser work, APIs, data, devices, and human approvals. Git is optional.

## Install the agent rule

Copy [AGENT.md](AGENT.md) into a project's `AGENTS.md`, or install `plugins/stash-work-plan` through the included Codex or Claude plugin manifest. Stash also serves the smaller `stash-work` skill through MCP Skills.

Configure the client with the Streamable HTTP endpoint:

```text
http://localhost:8080/mcp
```

## Session start

Call `resume_project` with three pieces of routing information:

- the exact project namespace
- a stable display name for this agent
- a short capability list such as `code`, `browser`, `research`, `document`, `data`, `device`, or `human`

The response contains the shared top-level goal, this agent's active work, at most three runnable candidates, project counts, one next action, and a `context_digest`. It does not require a local directory, Git repository, or MCP Roots.

Continue active work first. Otherwise select one candidate and call `resume_work(work_item_id)`. Its default brief contains only:

- the shared goal path
- the parent plan component, its outcome, and owned scopes
- the current item and next action
- pending completion conditions
- short evidence references, relevant non-fact memories, and linked resource summaries
- only facts added, updated, or removed since the previous digest
- final results from completed prerequisites
- unfinished blockers

Keep the returned `context_digest` and send it as `known_context_digest` next time. An unchanged view returns a small receipt instead of the same context. When `context_window.next_query` contains an offset or target digest, copy those fields into the next `resume_work` call until the response points at its current digest with offset zero. `input_bytes`, `input_limit_bytes`, and `truncated` make the model-input boundary explicit.

## Claim before acting

After selecting a prepared item, call `claim_work` immediately before any external or local action. It creates one execution attempt and an exclusive lease in a transaction. Keep the returned `lease_token` private.

One item cannot have two live attempts. The authenticated MCP principal controls namespace access; `agent_id`, capabilities, URLs, and local paths do not grant permission.

## Split newly discovered work

During an active attempt, call `spawn_work` when the item reveals another deliverable:

- `child`: a smaller result that belongs under the current item
- `prerequisite`: a result the current item needs first
- `related`: connected work that does not block the current item

The call creates the new item, its first action, completion conditions, capability hints, and graph edge together. Child and prerequisite work block the parent until they finish. This lets separate agents own A-1, A-2, and deeper work while every item remains connected to A.

## Link external work without copying it

Use `attach_work_resource` to connect a Jira issue, Confluence page, Git checkout, browser target, document, API, dataset, device, or artifact. Store only a short summary, stable key, revision, and URI. Never put connector credentials or a full external document in Stash.

When `authority` is `external`, the external system remains the source for human work. Stash remains the source for AI execution state, checkpoints, evidence, and handoffs. Connector polling and write-back are optional add-ons.

## Session end

After every meaningful action, call `checkpoint_work` with what was done, the observed result, and exactly one next action. For completion, submit evidence, verify every required condition, and call `finish_work` only after blockers are finished.

For unfinished work, call `handoff_work`. It records the current result and next action, ends the attempt, and makes the item available to a later agent. A stop hook may check for an accepted `handoff_work` or `finish_work` response, but it must not decide completion or copy a lease token into logs.

If a process ends unexpectedly, the lease remains until its expiry. A new agent calls `resume_project`, then `resume_work` for the same item. It waits for the live lease or follows the recorded handoff, and continues from `next_action`; it does not create a replacement item.

## Optional Git connector

Code projects may collect local facts and bind a checkout:

```bash
stash workspace facts --cwd . --agent-id codex --project-namespace /projects/myapp
```

`resolve_workspace`, `resume_workspace`, and `claim_workspace` add stable repository and worktree identity to the same execution record. `stash worktree sync` refreshes all registered worktrees. These tools are conveniences for Git-aware clients and are never required by the Web MCP project workflow.

## Child agent routing hints

An orchestrator may pass these values to a child agent:

```text
STASH_PROJECT_NAMESPACE
STASH_AGENT_ID
STASH_WORK_ITEM_ID
STASH_ATTEMPT_ID
```

Treat them as routing hints. The child still calls `resume_project` and `resume_work`; the server rechecks authentication, the current lease, and the latest checkpoint.
