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
cp .env.example .env
openssl rand -hex 32   # paste this into STASH_AUTH_API_SECRET in .env
# edit the provider API key and model settings in .env
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

Use the `/mcp` HTTP endpoint and configure the client to send the local bearer
token generated below.

### 2. Codex

The bundled Docker Compose profile uses token authentication. Generate a token
from the running container, then register the local Web MCP server:

```bash
export STASH_MCP_TOKEN="$(docker compose exec -T stash /stash mcp token --subject codex)"
codex mcp add stash-local --url http://127.0.0.1:8080/mcp --bearer-token-env-var STASH_MCP_TOKEN
```

You can also use the `stdio` transport and point it to the stash CLI binary:

```json
"stash": {
  "command": "stash",
  "args": ["mcp", "execute", "--with-consolidation", "--consolidate-namespaces", "/projects"]
}
```

For a remote MCP server, use Streamable HTTP. With the `oauth` profile, the
MCP client follows OAuth Authorization Code login and receives a Stash access
token bound to the MCP resource after the user approves the Stash access page:

```bash
codex mcp add stash --url https://stash.example.com/mcp
```

For unattended clients, set `STASH_AUTH_MODE=token` and
`STASH_AUTH_API_SECRET`, then issue a native bearer token:

```bash
export STASH_MCP_TOKEN="$(stash mcp token --subject codex)"
codex mcp add stash --url https://stash.example.com/mcp --bearer-token-env-var STASH_MCP_TOKEN
```

Authentication profiles:

- `none`: no HTTP authentication. Stash refuses to bind this mode beyond a
  loopback address.
- `oauth` (or the legacy alias `oidc`): OIDC login plus the MCP OAuth
  Authorization Code flow. MCP and SSE accept the resource-bound Stash OAuth
  access token and native Stash API bearer tokens.
- `token`: OIDC-free HTTP authentication using only Stash API bearer tokens.
- `stdio`: no MCP OAuth discovery. The local process is trusted, or it can
  validate `STASH_AUTH_STDIO_TOKEN` before using an isolated namespace.

HTTP MCP requests must send `Authorization: Bearer <stash_oauth_token>` or
`Authorization: Bearer <stash_api_token>`. The browser session cookie is only
for the embedded console; it is not the standard client credential.

The console's **Access settings** can issue a Stash API token after browser
login. For a server without OIDC, run `stash mcp token --subject <agent>` with
`STASH_AUTH_API_SECRET`; the command does not open the database or contact an
OIDC provider. The token lifetime follows `STASH_AUTH_TOKEN_TTL` (30 days by
default) and can be renewed with the same command.

OAuth access tokens expire after one hour by default; rotated refresh tokens
expire after 30 days. Configure them with `STASH_AUTH_ACCESS_TOKEN_TTL` and
`STASH_AUTH_REFRESH_TOKEN_TTL`. The access-token lifetime cannot exceed one
hour.

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

### 4. General MCP Clients (Cursor, Windsurf, OpenCode, Pi, etc.)

Prefer the Streamable HTTP URL `http://localhost:8080/mcp`. Use the SSE URL `http://localhost:8080/sse` only when the client does not support Streamable HTTP.

**Cursor** — `~/.cursor/mcp.json`

```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

**Windsurf** — `~/.codeium/windsurf/mcp_config.json`

```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

## Metrics and Health

`stash serve` uses one HTTP port for MCP, the web console, OAuth endpoints, metrics, and status checks (default `127.0.0.1:8080`). Docker also publishes port 8080 only on the host loopback interface. Prometheus metrics are available at `http://localhost:8080/metrics`; when HTTP authentication is enabled, `/metrics` requires the same bearer credential as MCP. `/healthz` and `/readyz` stay public for load-balancer probes. The metrics cover HTTP requests, authentication outcomes, MCP tool calls, outbound provider calls, namespace-scope decisions, consolidation backlog and latest errors, terminal result-memory coverage, and pending embedding retries. Request, authentication, tool, provider, and scope metrics use bounded labels and do not include user IDs or raw namespace names.

The HTTP contract is available as OpenAPI at `http://localhost:8080/openapi.json`; the interactive Swagger UI is at `http://localhost:8080/docs` (also `/swagger`). The UI loads its pinned viewer assets from jsDelivr, while the specification remains available same-origin for offline tooling.

If the embedding endpoint still fails after the SDK's short request retries, Stash saves the raw memory with a pending index state. It also preserves the raw memory when PostgreSQL is reachable but the vector value cannot be stored. The background worker retries without an attempt limit; exponential backoff stops growing at the configured maximum. Configure it with `STASH_EMBEDDING_RETRY_INTERVAL`, `STASH_EMBEDDING_RETRY_MAX_INTERVAL`, and `STASH_EMBEDDING_RETRY_BATCH_SIZE`.

The web console can expose an **Embedding maintenance** page when `STASH_ADMIN_SUBJECTS` (comma-separated OIDC subjects) or `STASH_ADMIN_TOKEN` is configured. It shows pending work, the current model and dimension, and the latest provider error. **Retry pending** wakes scheduled failures without interrupting active work. **Reindex all** clears stored vectors and the disposable cache, then queues every live episode and fact while preserving the original content.

MCP tool results keep a 32 KiB safety cap by default (this is a transport safeguard, not a model context size). Oversized list pages are split into `items`, `has_more`, and `next_offset`; call the same tool with `offset=next_offset` to continue. Oversized non-list results return a small notice before they can exhaust the client context. Change the cap with `STASH_MCP_MAX_RESPONSE_BYTES`.

Reasoner and embedding limits are separate from the MCP response cap. Set `STASH_REASONER_CONTEXT_TOKENS` to the full context window of the configured reasoning model and `STASH_REASONER_RESERVED_TOKENS` to the space kept for instructions and the JSON answer. Set `STASH_EMBEDDING_CONTEXT_TOKENS` to the embedding model's input window. Stash converts these token budgets to a conservative UTF-8 byte budget, prefers paragraph breaks and then sentence endings, and only hard-splits when no natural boundary fits. Long embedding chunks are combined into one vector. MCP does not publish the model tokenizer or the caller's remaining context; when a limit is left at `0`, Stash learns a provider-reported limit when possible and adapts after a context-length error. For a 44,544-token reasoner, for example, start with `STASH_REASONER_CONTEXT_TOKENS=44544` and a reserve of `4096` or more.

Provider requests and MCP tool handlers have a two-minute default deadline. Set `STASH_OPENAI_REQUEST_TIMEOUT` and `STASH_MCP_TOOL_TIMEOUT` higher for a slower local model; when a deadline is reached, the call returns an error instead of holding the client until its own timeout, and failed embeddings remain queued for retry. Each row is paused after five failed provider attempts until an operator retries it, so a broken provider cannot consume a small daily quota indefinitely. `recall` still searches saved text with PostgreSQL trigram matching while the embedding provider is unavailable, including a fact's `entity`, `property`, and `value` fields.

When `STASH_EMBEDDING_MODEL` or `STASH_VECTOR_DIM` changes, Stash detects the change at startup. It resizes the pgvector columns when needed, clears old vectors and the embedding cache, and queues every live episode and fact for indexing with the new model. The original content is preserved; the background worker recomputes vectors and retries provider failures. Use `stash reindex --dry-run` to inspect a manual reindex, or `stash reindex` when you want to start one immediately without changing the model setting.

At `info`, Stash logs completed MCP/API calls and OpenAI-compatible provider calls. Successful `queue_prompt_history` calls stay at `debug` because they occur for every submitted prompt. Set `STASH_LOG_LEVEL=debug` to include them and ordinary web access records; failed requests are promoted to `warn`. Logs include the method, bounded path, status, duration, component/model, and request ID when available, but never query strings, authorization headers, cookies, or request bodies.

### Auto-Save &amp; Seamless Handoff

The bundled Codex and Claude Code plugin gives one memory reminder at session startup and queues each submitted prompt without waiting for embedding. It reuses the client's authenticated MCP connection and stores exact prompt text, so review the privacy and opt-out notes in the [**Seamless Agent Handoff Guide**](docs/AGENT_HANDOFF.md).

## What It Does

Stash is a cognitive layer between your AI agent and the world. Episodes become facts. Facts become relationships. Relationships become patterns. Patterns become wisdom.

A 9-stage consolidation pipeline turns raw observations into structured knowledge — facts, relationships, causal links, patterns, contradictions, goal tracking, failure patterns, and hypothesis verification. Each stage only processes new data since the last run.

## Shared Work Map and Optional Connectors

Work cards live separately from memory data and connect goals, tasks, dependencies, resources, and activity events in one graph. A project can select one shared top-level goal, decompose it into A-1, A-2, and deeper outcomes, and show memory and external resources flowing through work into that shared outcome. The Goal Map shows progress, active agents, blockers, next actions, recent results, and the Jira, Confluence, Git, browser, document, API, data, or device references attached to each item.

The web console's Work Graph lists `/projects/<name>` namespaces as project scopes. Selecting a project limits the graph to that project and its descendants; `All projects` shows work under `/projects` together. MCP clients can pass `project: "/projects/myapp"` to `get_work_graph` to retrieve one project explicitly.

For an owner-facing living plan, use the Work Plan API instead of a changing `PLAN.md`. Its stable component cards hold executable child tasks, directed prerequisites, optional connector references, and decisions made before implementation. `get_work_plan` is the shared current plan; `create_plan_component`, `update_plan_component`, `create_plan_task`, `update_plan_task`, task-state tools, `link_plan_components`, and `record_plan_decision` update it. `validate_work_plan` runs an explicit semantic review with the configured Reasoner model and stores the latest result; it does not use the embedding model. When plan content changes, `get_work_plan.validation.stale` marks the saved review as outdated. The ordinary issue board remains for ad-hoc local issues and does not mix in plan-managed cards.

Call `resume_project(namespace, agent_id, capabilities)` only when continuing an existing Stash item or a user-requested shared Work Plan. It returns that agent's active work and at most three runnable candidates without a local path, Git repository, or MCP Roots. After choosing an item, call `resume_work` once for a bounded brief containing its goal path, parent plan component, owned scopes, next action, pending conditions, relevant memory, linked resource summaries, completed prerequisite results, and blockers. Do not refresh it unless Stash reports a conflict or stale state. `claim_work` grants one exclusive lease immediately before action.

During an active lease, `spawn_work` creates a child, prerequisite, or related item with its first action and completion conditions. Child and prerequisite items block the parent until they finish, so many agents can decompose A-1 and A-2 without losing the common outcome. Submit one evidence record for every condition proved by the same observation, then pass the successfully proved pending IDs to `finish_work.passed_condition_ids`. The finish call accepts only IDs backed by current-attempt evidence and is rejected while evidence or blockers are missing.

`attach_work_resource` stores a small reference instead of copying an entire source. A Jira issue or Confluence page can remain authoritative for human work while Stash records AI work, links the two, and shows both in the Goal Map. The current build provides the neutral resource model; connector polling and write-back are optional add-ons and are not required for the work loop.

Git worktree commands remain available as an optional connector for code projects. They are never a prerequisite for Web MCP work.

```bash
stash workspace facts --cwd . --agent-id codex --project-namespace /projects/myapp
stash worktree sync --repo . --namespace /projects/myapp
stash worktree list --namespaces /projects/myapp
stash issue create --namespace / "Login error" --type bug --labels auth,login
stash issue list --namespaces / --status doing --label auth
stash issue comment add W-000001 --body "Reproduction confirmed"
```

An agent rules sample is available at [docs/AGENT.md](docs/AGENT.md). The default sequence is `resume_project` → `resume_work` → `claim_work` → batched evidence → `finish_work`. Do not call Stash after routine commands or edits; checkpoint only at interruption, lease-risk, or handoff boundaries. Use `spawn_work` when execution reveals another result that must be delivered.

### Work plan skill

The repository includes the same MCP workflow as a Codex and Claude plugin. Configure a Stash MCP server named `stash`, then install the plugin from this repository:

```bash
claude plugin marketplace add wwwhana/stash
claude plugin install stash-work-plan@stash-tools

codex plugin marketplace add wwwhana/stash
codex plugin add stash-work-plan@stash-tools
```

Claude exposes the plugin skill as `/stash-work-plan:stash-work-plan`; Codex exposes it as `$stash-work-plan`.
Both plugins use the same `hooks/hooks.json`. It adds one local memory reminder
at a new session startup, queues submitted prompts through the authenticated
`stash` MCP connection, and requires a successful `finish_work` or
`handoff_work` before claimed work can normally stop. Review and trust the
changed hooks in Codex's `/hooks`; verify that they are listed in Claude Code's
`/hooks`.

The Streamable HTTP endpoint also serves the built-in `stash-work` instructions through the experimental `io.modelcontextprotocol/skills` extension described by draft SEP-2640. Clients that implement the draft can discover it with `skills/list`; other clients can keep using the Codex or Claude plugin above. The extension is advertised only on `/mcp`, where its custom methods are reachable.

## License

Apache 2.0
