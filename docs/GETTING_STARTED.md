# Getting Started with Stash

You ran `docker compose up`. Here's what to do next.

## 1. Verify Stash is running

```bash
docker compose ps
docker compose logs stash | tail -20
```

You should see the MCP server listening on port **8080**. The SSE endpoint is:

```
http://localhost:8080/sse
```

At the default `STASH_LOG_LEVEL=info`, `docker compose logs stash` also shows
completed MCP/API and model-provider calls. Use `STASH_LOG_LEVEL=debug` for
ordinary web requests; failed calls are shown at `warn`. Request bodies,
authorization headers, cookies, and query strings are not logged.

Quick check (expects HTTP 200 or SSE handshake):

```bash
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/sse
```

## 2. Connect your MCP client

Point any MCP-over-SSE client at `http://localhost:8080/sse`.

**Cursor** — create or edit `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/sse"
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

For project work in a Git checkout, collect facts without opening the database:

```bash
stash workspace facts --cwd . --agent-id codex --project-namespace /projects/myapp
```

Pass that JSON to `resolve_workspace`, then call `resume_workspace` with the returned namespace and worktree ID. Prepare an existing item's completion conditions only when needed, and call `claim_workspace` immediately before implementation. Later sessions omit `project_namespace`; Stash resolves the saved repository binding.

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

### HTTP MCP authentication

Use `STASH_AUTH_MODE=oauth` for a remote Streamable HTTP or SSE server. Set
`STASH_AUTH_ISSUER` to the OIDC provider's issuer, configure the browser client
(`STASH_AUTH_CLIENT_ID`, `STASH_AUTH_CLIENT_SECRET`, and
`STASH_AUTH_REDIRECT_URL`), and set `STASH_AUTH_MCP_RESOURCE_URL` to the public
`/mcp` URL. Stash exposes the protected-resource and authorization-server
metadata, then performs Authorization Code + PKCE and issues a resource-bound
Bearer token. `/oauth/register` accepts native public-client registrations.

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
- Use `http://localhost:8080/sse` (not `https`) for local Docker.

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
