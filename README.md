# Stash

[🇺🇸 English](README.md) | [🇰🇷 한국어](README.ko.md)

> **Note:** This is an enhanced fork of the original [alash3al/stash](https://github.com/alash3al/stash). 
> This version (`wwwhana/stash`) introduces significant improvements including Hybrid Search (Vector + Trigram RRF), deterministic episode clustering, safer soft-delete lifecycle, and advanced MCP auto-hydration hooks.

**Your AI has amnesia. We fixed it.**

Every LLM starts every conversation from zero. Stash gives your agent persistent memory — it remembers, recalls, consolidates, and learns across sessions. No more explaining yourself from scratch.

Open source. Self-hosted. Works with any MCP-compatible agent.

---

## Quick Start

```bash
git clone https://github.com/wwwhana/stash.git
cd stash
cp .env.example .env   # edit with your API key + model
docker compose up
```

That's it. Postgres + pgvector, migrations, MCP/metrics servers, and background consolidation — all in one command.

**Next:** [Getting Started guide](docs/GETTING_STARTED.md) — connect your MCP client, run `init` / `remember` / `recall`, and verify everything works.

**Fully local (no cloud API):** [Ollama setup guide](docs/LOCAL_OLLAMA.md) — host Ollama + Docker Compose, private embeddings and reasoner.

## LLM Provider Setup (OpenAI Default &amp; Local Example)

Stash relies on an external LLM provider for vectorization and reasoning. You can use standard cloud providers (like OpenAI) or a local setup (like Ollama).

### Default (OpenAI)

Set your `.env` like this:

```bash
STASH_OPENAI_API_KEY=sk-your-openai-api-key
STASH_EMBEDDING_MODEL=text-embedding-3-small
STASH_REASONER_MODEL=gpt-4o-mini
STASH_VECTOR_DIM=1536
```

### Local/Custom LLMs (Ollama, LM Studio)

To point Stash to a local or custom OpenAI-compatible server, specify the base URL.
The API key may be left empty when that endpoint does not require authentication.
**Important Tuning Note:** If you are using `multilingual-e5-small` or similar models, make sure you match the `STASH_VECTOR_DIM` to the model's output dimensions (e.g., `384`).

```bash
STASH_OPENAI_BASE_URL=http://host.docker.internal:11434/v1
STASH_OPENAI_API_KEY=
STASH_EMBEDDING_MODEL=multilingual-e5-small
STASH_REASONER_MODEL=llama3
STASH_VECTOR_DIM=384
```

See [Getting Started](docs/GETTING_STARTED.md) for a fuller configuration checklist.

## MCP Client Setup

After `docker compose up`, Stash exposes an MCP server over HTTP/SSE. It supports both Streamable HTTP (via `/mcp`) and standard SSE (via `/sse`).

### 1. Claude Code

Uses the `/mcp` HTTP endpoint natively.

```bash
claude mcp add stash http://localhost:8080/mcp
```

### 2. Codex

For a local process, use the `stdio` transport and point it to the stash CLI binary:

```json
"stash": {
  "command": "stash",
  "args": ["mcp", "execute", "--with-consolidation"]
}
```

For a remote, OAuth-protected MCP server, use Streamable HTTP:

```bash
codex mcp add stash --url https://stash.example.com/mcp --oauth-client-id stash-codex
codex mcp login stash
```

Set `STASH_AUTH_MODE=oauth`, the OIDC issuer, browser client settings, and
`STASH_AUTH_MCP_RESOURCE_URL` to the public `/mcp` URL. Stash publishes the
MCP Protected Resource Metadata and brokers Authorization Code + PKCE through
the configured OIDC provider. Dynamic public-client registration is available
at `/oauth/register`; manually registered clients may use the same endpoints.

Authentication profiles:

- `none`: no HTTP authentication. Use only for an isolated local instance.
- `oauth` (or the legacy alias `oidc`): OAuth 2.1 Bearer access tokens for
  Streamable HTTP and SSE, with Protected Resource Metadata, PKCE, refresh
  token rotation, and optional dynamic client registration.
- `stdio`: no MCP OAuth discovery. The local process is trusted, or it can
  validate `STASH_AUTH_STDIO_TOKEN` before using an isolated namespace.

HTTP MCP requests must send `Authorization: Bearer <access-token>`. The
browser session cookie is only for the embedded console; it is not the
standard client credential.

### 3. agy (Antigravity)

Configure via `~/.gemini/config/mcp_config.json`:

```json
{
  "mcpServers": {
    "stash": {
      "serverUrl": "http://localhost:8080/sse"
    }
  }
}
```

### 4. General SSE Clients (Cursor, Windsurf, OpenCode, Pi, etc.)

Point any MCP-compatible client at the SSE URL: `http://localhost:8080/sse`

**Cursor** — `~/.cursor/mcp.json`

```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/sse"
    }
  }
}
```

**Windsurf** — `~/.codeium/windsurf/mcp_config.json`

```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/sse"
    }
  }
}
```

## Metrics and Health

`stash serve` uses one HTTP port for MCP, the web console, OAuth endpoints, metrics, and status checks (default `:8080`). Prometheus metrics are available at `http://localhost:8080/metrics`; `/healthz` checks the database connection and `/readyz` checks readiness. The metrics cover HTTP requests, authentication outcomes, MCP tool calls, namespace-scope decisions, consolidation activity, and pending embedding retries. Request, authentication, tool, and scope metrics use bounded labels and do not include user IDs or raw namespace names.

If the embedding endpoint still fails after the SDK's short request retries, Stash saves the raw memory with a pending index state. It also preserves the raw memory when PostgreSQL is reachable but the vector value cannot be stored. The background worker retries without an attempt limit; exponential backoff stops growing at the configured maximum. Configure it with `STASH_EMBEDDING_RETRY_INTERVAL`, `STASH_EMBEDDING_RETRY_MAX_INTERVAL`, and `STASH_EMBEDDING_RETRY_BATCH_SIZE`.

MCP tool results keep a 32 KiB safety cap by default (this is a transport safeguard, not a model context size). Oversized list pages are split into `items`, `has_more`, and `next_offset`; call the same tool with `offset=next_offset` to continue. Oversized non-list results return a small notice before they can exhaust the client context. Change the cap with `STASH_MCP_MAX_RESPONSE_BYTES`.

Reasoner and embedding limits are separate from the MCP response cap. Set `STASH_REASONER_CONTEXT_TOKENS` to the full context window of the configured reasoning model and `STASH_REASONER_RESERVED_TOKENS` to the space kept for instructions and the JSON answer. Set `STASH_EMBEDDING_CONTEXT_TOKENS` to the embedding model's input window. Stash converts these token budgets to a conservative UTF-8 byte budget, prefers paragraph breaks and then sentence endings, and only hard-splits when no natural boundary fits. Long embedding chunks are combined into one vector. MCP does not publish the model tokenizer or the caller's remaining context; when a limit is left at `0`, Stash learns a provider-reported limit when possible and adapts after a context-length error. For a 44,544-token reasoner, for example, start with `STASH_REASONER_CONTEXT_TOKENS=44544` and a reserve of `4096` or more.

Set `STASH_LOG_LEVEL=debug` to emit HTTP access records. Access records omit query strings, authorization headers, and cookies.

### Auto-Save &amp; Seamless Handoff

To make your AI agents automatically save their memory and pass the baton between sessions without manual prompting, read the [**Seamless Agent Handoff Guide**](docs/AGENT_HANDOFF.md). You can configure Cursor, Claude Desktop, or Antigravity IDE to mechanically load and save context.

## What It Does

Stash is a cognitive layer between your AI agent and the world. Episodes become facts. Facts become relationships. Relationships become patterns. Patterns become wisdom.

A 9-stage consolidation pipeline turns raw observations into structured knowledge — facts, relationships, causal links, patterns, contradictions, goal tracking, failure patterns, and hypothesis verification. Each stage only processes new data since the last run.

## Work Graphs and Git Worktrees

Work cards live separately from memory data and connect goals, tasks, dependencies, worktrees, and activity events in one graph. Issue types (bug, feature, task), labels, assignees, and comments are included, so the same data can serve as a local issue tracker without another service. The same data can be shown as a Kanban board grouped by status or as a dependency graph. Work cards can also link to facts, failures, and hypotheses so an agent can pick up the evidence it needs in a later session.

For an owner-facing living plan, use the Work Plan API instead of a changing `PLAN.md`. Its 5–9 stable component cards own repository paths and hold executable child tasks, directed prerequisites, worktree links, and decisions made before implementation. `get_work_plan` is the shared current plan; `create_plan_component`, `update_plan_component`, `create_plan_task`, `update_plan_task`, task-state tools, `link_plan_components`, and `record_plan_decision` update it. `validate_work_plan` runs an explicit semantic review with the configured Reasoner model and stores the latest result; it does not use the embedding model. When plan content changes, `get_work_plan.validation.stale` marks the saved review as outdated. The ordinary issue board remains for ad-hoc local issues and does not mix in plan-managed cards.

An agent-side bridge can sync the repository's local Git worktrees into Stash. Git remains the source for code and diffs; Stash stores paths, branches, commits, status, and work history.

```bash
stash worktree sync --repo . --namespace /projects/myapp
stash worktree list --namespaces /projects/myapp
stash issue create --namespace / "Login error" --type bug --labels auth,login
stash issue list --namespaces / --status doing --label auth
stash issue comment add W-000001 --body "Reproduction confirmed"
```

An agent rules sample is available at [docs/AGENT.md](docs/AGENT.md). MCP clients can use `get_work_plan`, `validate_work_plan`, `create_plan_component`, `update_plan_component`, `create_plan_task`, `update_plan_task`, `start_plan_task`, `complete_plan_task`, `block_plan_task`, `unblock_plan_task`, `set_plan_component_paths`, `link_plan_components`, and `record_plan_decision` for a living plan; `create_work_item`, `list_work_items`, `add_work_item_dependency`, `get_work_graph`, `add_work_item_comment`, `list_work_item_comments`, `link_work_item_memory`, `list_work_item_memory_links`, `list_worktrees`, and `record_work_event` remain available for local issue tracking. Call `init` once to create the default workspace before syncing or creating work data.

### Work plan skill

The repository includes the same MCP workflow as a Codex and Claude plugin. Configure a Stash MCP server named `stash`, then install the plugin from this repository:

```bash
claude plugin marketplace add wwwhana/stash
claude plugin install stash-work-plan@stash-tools

codex plugin marketplace add wwwhana/stash
codex plugin add stash-work-plan@stash-tools
```

Claude exposes the plugin skill as `/stash-work-plan:stash-work-plan`; Codex exposes it as `$stash-work-plan`.

## License

Apache 2.0
