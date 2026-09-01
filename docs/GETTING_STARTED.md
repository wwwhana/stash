# Getting Started with Stash

You ran `docker compose up`. Here's what to do next.

## 1. Verify Stash is running

```bash
docker compose ps
docker compose logs stash | tail -20
```

You should see the MCP server listening on port **8080**. The primary Web MCP endpoint is:

```
http://localhost:8080/mcp
```

At the default `STASH_LOG_LEVEL=info`, `docker compose logs stash` also shows
completed MCP/API and model-provider calls. Use `STASH_LOG_LEVEL=debug` for
ordinary web requests; failed calls are shown at `warn`. Request bodies,
authorization headers, cookies, and query strings are not logged.

Quick server check:

```bash
curl -sS http://localhost:8080/healthz
```

## 2. Connect your MCP client

Point a Streamable HTTP client at `http://localhost:8080/mcp`. The older `/sse` endpoint remains available for clients that only support MCP over SSE.

**Codex**:

```bash
codex mcp add stash-local --url http://127.0.0.1:8080/mcp
```

**Cursor** — create or edit `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

Restart Cursor (or reload MCP). Stash should appear in your MCP tool list.

See [README MCP Client Setup](../README.md#mcp-client-setup) for Claude Desktop, Windsurf, and OpenCode configs.

## 3. First session workflow

Once connected, ask your agent to use Stash tools in this order:

| Step | MCP tool | What it does |
|------|----------|--------------|
| 1 | `init` | Creates `/self` namespace scaffold (capabilities, limits, preferences) |
| 2 | `remember` | Store an episode or fact ("User prefers Python", "Project uses Postgres") |
| 3 | `recall` | Semantic search over what you stored |
| 4 | `consolidate` | Run the pipeline that turns episodes into structured facts |

Example prompts to your agent:

- *"Call Stash `init` to set up memory namespaces."*
- *"Use Stash `remember` to store: this project uses Stash on port 8080 with pgvector."*
- *"Use Stash `recall` to find what you know about this project's stack."*
- *"Run Stash `consolidate` to process recent episodes into facts."*

For shared project work, first create a namespace such as `/projects/myapp`, create the top-level outcome, and select it as that project's shared goal. Then have every agent follow this sequence:

1. Call `resume_project` with the exact namespace, its `agent_id`, and a short capability list.
2. Continue active work or choose one of the returned candidates, then call `resume_work` for that item.
3. Call `claim_work` immediately before acting.
4. Record observed results with `checkpoint_work`. Use `spawn_work` when a child or prerequisite is discovered.
5. Submit and verify completion evidence, then call `finish_work`; use `handoff_work` when stopping unfinished work.

This flow is entirely Web MCP based and does not require a local path, Git repository, or MCP Roots. For code projects, `stash workspace facts`, `resolve_workspace`, `resume_workspace`, and `claim_workspace` remain optional Git connector helpers.

Use `attach_work_resource` to add a short Jira, Confluence, Git, browser, document, API, data, or device reference to the relevant work item. Keep external bodies and credentials in their original systems.

## 4. Background consolidation

When started with `stash serve` (the Docker Compose default), Stash consolidates in the background. You can also trigger manually via the `consolidate` MCP tool or CLI:

```bash
docker compose exec stash /stash consolidate run
```

## 5. Configuration checklist

If tools fail, check `.env`:

| Variable | Purpose |
|----------|---------|
| `STASH_OPENAI_API_KEY` | Embeddings + reasoner; optional for endpoints without authentication |
| `STASH_OPENAI_BASE_URL` | API base URL |
| `STASH_OPENAI_REQUEST_TIMEOUT` | Maximum time for one provider request attempt (default `2m`) |
| `STASH_EMBEDDING_MODEL` | Must match `STASH_VECTOR_DIM` (1536 for `text-embedding-3-small`) |
| `STASH_VECTOR_DIM` | Output dimension of the embedding model; changing it on restart automatically queues a full reindex |
| `STASH_EMBEDDING_RETRY_INTERVAL` | How often pending embeddings are retried (default `1m`) |
| `STASH_EMBEDDING_RETRY_MAX_INTERVAL` | Maximum exponential backoff (default `1h`) |
| `STASH_EMBEDDING_RETRY_BATCH_SIZE` | Maximum pending rows considered per pass (default `100`) |
| `STASH_EMBEDDING_CONTEXT_TOKENS` | Embedding model input window; `0` uses adaptive splitting after a provider context error |
| `STASH_ADMIN_SUBJECTS` | Comma-separated OIDC subjects allowed to open embedding maintenance |
| `STASH_ADMIN_TOKEN` | Optional separate token for embedding maintenance (`X-Stash-Admin-Token`) |
| `STASH_REASONER_MODEL` | Model used for consolidation and `validate_work_plan` |
| `STASH_REASONER_CONTEXT_TOKENS` | Full reasoning-model context window; `0` uses adaptive splitting after a provider context error |
| `STASH_REASONER_RESERVED_TOKENS` | Tokens kept for instructions and the JSON answer (default `4096`) |
| `STASH_MCP_MAX_RESPONSE_BYTES` | Maximum JSON bytes in one MCP tool result (default `32768`); large pages return `next_offset` |
| `STASH_MCP_TOOL_TIMEOUT` | Maximum time for one MCP tool call (default `2m`) |
| `STASH_AUTH_MODE` | `none`, `token`, `oauth`, or `stdio` |
| `STASH_AUTH_API_SECRET` | Secret used to sign Stash API tokens |
| `STASH_AUTH_TOKEN_TTL` | Lifetime of issued API tokens (default `720h`) |

### HTTP MCP authentication

Use `STASH_AUTH_MODE=token` and `STASH_AUTH_API_SECRET` for a remote
Streamable HTTP or SSE server. Issue a token without OIDC or database access:

```bash
stash mcp token --subject agent-1
```

Send the result as `Authorization: Bearer <stash_api_token>`. MCP verifies this
Stash token directly and does not start OIDC discovery. `STASH_AUTH_MODE=oauth`
is still available when the browser console needs OIDC login; MCP requests use
the Stash token there as well. `STASH_AUTH_TOKEN_TTL` controls token lifetime.

For a local CLI process, use `STASH_AUTH_MODE=stdio`; STDIO does not use MCP
OAuth discovery. `STASH_AUTH_MODE=none` disables HTTP authentication and is
intended only for an isolated local instance.

### Embedding maintenance

The web console can expose an **Embedding maintenance** page when
`STASH_ADMIN_SUBJECTS` or `STASH_ADMIN_TOKEN` is configured. It shows pending
rows, rows ready now, the latest provider error, model, and vector dimension.
**Retry pending** wakes scheduled failures without interrupting active work.
**Reindex all** clears stored vectors and the disposable cache, then queues
every live episode and fact while preserving their original content.

**Running fully local?** See [LOCAL_OLLAMA.md](LOCAL_OLLAMA.md) — Ollama on the host, no cloud API key.

**Using Atlas Cloud?** Stash already supports it through the OpenAI-compatible API:

```bash
STASH_OPENAI_API_KEY=your-atlas-cloud-api-key
STASH_OPENAI_BASE_URL=https://api.atlascloud.ai/v1
STASH_EMBEDDING_MODEL=text-embedding-3-small
STASH_REASONER_MODEL=deepseek-ai/DeepSeek-V3-0324
STASH_VECTOR_DIM=1536
```

Atlas Cloud docs: [https://www.atlascloud.ai/docs](https://www.atlascloud.ai/docs)

## Troubleshooting

**MCP client can't connect**

- Confirm `docker compose up` finished and port 8080 is not in use elsewhere.
- Use `http://localhost:8080/mcp` (not `https`) for local Docker. Try `/sse` only for a client that does not support Streamable HTTP.

**Empty recall results**

- Run `init` first, then `remember`, then `recall`.
- Cloud endpoints require a valid API key; local endpoints without authentication may leave it empty.

**Consolidation does nothing**

- Needs episodes via `remember` first.
- Check logs: `docker compose logs stash`.

**Provider returns auth or model errors**

- Confirm `STASH_OPENAI_BASE_URL` points to the correct provider endpoint.
- Confirm the embedding and reasoner model names match what your provider exposes.
- Atlas Cloud users should use `https://api.atlascloud.ai/v1` and a valid API key.

## Next steps

- Explore namespaces: `list_namespaces`, `create_namespace`
- Track goals and failures: `create_goal`, `create_failure`
- Full tool list: see the [docs site MCP section](https://alash3al.github.io/stash/#mcp)
