# Workspace Resume and Handoff

Stash can resolve a Git checkout to its project and return the bounded state a new agent needs. The agent still decides when work is complete; hooks must not mark work done or create issues on their own.

## Install the agent rule

Copy [AGENT.md](AGENT.md) into the repository's `AGENTS.md`, or install `plugins/stash-work-plan` through the included Codex or Claude plugin manifest. Stash also serves the smaller `stash-work` skill through MCP Skills.

## Session start

Collect Git facts locally:

```bash
stash workspace facts --cwd . --agent-id codex
```

This command does not connect to PostgreSQL. On first use it creates two local Git settings when supplied:

- `stash.repositoryInstanceId`: a random ID that remains stable when the clone moves
- `stash.projectNamespace`: the owner's explicit first binding

The session-start integration then passes the JSON fields to `resolve_workspace` and calls `resume_workspace` with the returned namespace and worktree ID. The resume response includes:

- the current component plan
- doing and blocked work
- dependency graph
- current worktree and active attempt
- latest checkpoint and handoff
- recent decisions and failures
- project context
- one next action when one is available

A lifecycle hook may inject the local JSON and an instruction to call these MCP tools. A shell hook cannot safely impersonate an OAuth-authenticated MCP principal, so Stash does not ship a fake `curl` login flow.

## Claim before implementation

After choosing an existing prepared item, call `claim_workspace` with its ID and the same Git facts. The server performs these changes in one transaction:

1. resolve or update the repository binding
2. resolve or update the stable worktree
3. attach the worktree to the item
4. create the attempt and exclusive lease
5. return the workspace state and private lease token

One item cannot have two live attempts, and one worktree cannot claim two items. The authenticated MCP principal, not the local path or agent name, controls namespace access.

## Heartbeat and repository sync

Every `resolve_workspace` or `claim_workspace` call refreshes the current worktree heartbeat. For a complete repository scan, run:

```bash
stash worktree sync --repo . --namespace /projects/myapp --agent-id codex
```

Use `--namespace` for the first binding. Later syncs read `stash.projectNamespace` from the shared Git config, so the flag can be omitted.

The full sync marks registered worktrees missing when Git no longer lists them. Background maintenance marks heartbeats stale after 24 hours and missing worktrees removed after seven days. A later valid heartbeat restores the worktree.

## Session end

For unfinished work, call `handoff_work` before the agent exits. It records the observed result and exactly one next action while releasing the lease. For finished work, submit and verify evidence, then call `finish_work`.

A stop hook may check that the transcript contains an accepted `handoff_work` or `finish_work` response. It must not manufacture a checkpoint, copy a lease token into logs, or decide that work is done. If the network is unavailable, keep the local transcript and retry the same action key after connectivity returns.

## Child agents and worktrees

Pass these values to a child agent when the orchestrator supports environment injection:

```text
STASH_PROJECT_NAMESPACE
STASH_WORKTREE_ID
STASH_AGENT_ID
STASH_WORK_ITEM_ID
STASH_ATTEMPT_ID
```

Treat them as routing hints. The child must still call `resolve_workspace` and `resume_workspace`; the server rechecks authentication, binding, active lease, and current checkpoint.
