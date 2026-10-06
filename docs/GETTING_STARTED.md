# Getting Started with Stash

Before the first start, generate a signing secret with `openssl rand -hex 32`.
Set the server's `.env` to token mode, paste the generated value once, and keep
it stable. Changing the secret invalidates previously issued tokens.

```dotenv
STASH_AUTH_MODE=token
STASH_AUTH_API_SECRET=<output of openssl rand -hex 32>
```

Then run `docker compose up`. If the server was already running when these
values changed, recreate the Stash service so it receives the new environment:

```bash
docker compose up -d --force-recreate stash
```

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
completed MCP/API and model-provider calls. Successful `queue_prompt_history`
calls are kept at `debug` because they occur for every submitted prompt. Use
`STASH_LOG_LEVEL=debug` for those calls and ordinary web requests; failed calls
are shown at `warn`. Request bodies, authorization headers, cookies, and query
strings are not logged.

Quick server check:

```bash
curl -sS http://localhost:8080/healthz
```

Open `http://localhost:8080/docs` for the Swagger UI, or fetch the same-origin
OpenAPI document directly:

```bash
curl -sS http://localhost:8080/openapi.json
```

## 2. Connect your MCP client

Point a Streamable HTTP client at `http://localhost:8080/mcp`. The older `/sse` endpoint remains available for clients that only support MCP over SSE.

### Codex: token, MCP server, and plugin

The Docker Compose profile requires a bearer token. Issue a 30-day token from
the running server. The command prints the token once; do not put the returned
value in documentation, source control, or chat:

```bash
docker compose exec -T stash /stash mcp token --subject codex --ttl 720h
```

Set `STASH_MCP_TOKEN` on the computer that runs Codex. For Stash and terminal
Codex on the same machine, issue it directly into the current shell without
printing it again:

```bash
export STASH_MCP_TOKEN="$(docker compose exec -T stash /stash mcp token --subject codex --ttl 720h)"
```

For the macOS Codex app, read a token supplied by the server operator without
echoing it and add it to the current login session before fully quitting and
reopening Codex:

```zsh
read -s "STASH_MCP_TOKEN?Stash token: "
echo
export STASH_MCP_TOKEN
launchctl setenv STASH_MCP_TOKEN "$STASH_MCP_TOKEN"
```

`launchctl setenv` does not survive logout or restart. Set it again before
launching Codex after a new login. Avoid saving the token as plain text in a
shell profile.

Register the MCP server with the exact name `stash`; the plugin hooks use this
name. Use one of these URLs:

```bash
# Local Stash
codex mcp add stash --url http://127.0.0.1:8080/mcp --bearer-token-env-var STASH_MCP_TOKEN

# Remote Stash over HTTPS
codex mcp add stash --url https://stash.example.com/mcp --bearer-token-env-var STASH_MCP_TOKEN
```

If an old `stash` entry has the wrong URL or authentication setting, remove that
entry and add it again:

```bash
codex mcp remove stash
```

Install the bundled work-plan plugin, then start a new Codex session:

```bash
codex plugin marketplace add wwwhana/stash
codex plugin add stash-work-plan@stash-tools
```

Open `/hooks` in Codex and review and trust the installed hooks. Confirm both
registrations from the terminal:

```bash
codex mcp get stash
codex plugin list
```

Stash uses `STASH_MCP_TOKEN` in every profile; do not run
`codex mcp login stash`. Stash does not act as an OAuth server for MCP
clients, so that login flow has nothing to talk to.

### Cursor

Create or edit `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "stash": {
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer ${env:STASH_MCP_TOKEN}"
      }
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
| 1 | `init` | Creates `/self` namespace scaffold (capabilities, prompt history, limits, preferences) |
| 2 | `remember` | Store an episode or fact ("User prefers Python", "Project uses Postgres") |
| 3 | `recall` | Semantic search over what you stored |
| 4 | `consolidate` | Run the pipeline that turns episodes into structured facts |

Example prompts to your agent:

- *"Call Stash `init` to set up memory namespaces."*
- *"Use Stash `remember` to store: this project uses Stash on port 8080 with pgvector."*
- *"Use Stash `recall` to find what you know about this project's stack."*
- *"Run Stash `consolidate` for `/projects/myapp` to process recent episodes into facts."*

For shared project work, first create a namespace such as `/projects/myapp`, create the top-level outcome, and select it as that project's shared goal. Then have every agent follow this sequence:

1. If the exact work item ID is known, skip project lookup. Otherwise call `resume_project` once with the exact namespace, its `agent_id`, and a short capability list.
2. Continue active work or choose one returned candidate, then call `resume_work` once for that item.
3. Call `claim_work` immediately before acting.
4. Work without per-command Stash calls. Use `checkpoint_work` only before interruption, lease risk, or a recoverable partial result. Use `spawn_work` when a child or prerequisite is discovered.
5. If the work produced a distinct decision, correction, failure, or lesson, combine related details in one `remember_work` call.
6. Submit each observation once with every condition ID it proves, then put the successfully proved pending IDs in `finish_work.passed_condition_ids`. The finish call accepts only IDs backed by evidence from the current attempt. Use `verify_work_condition` only for a waiver or early acceptance, and `handoff_work` when stopping unfinished work.

Do not run this tracked-work sequence for unrelated ordinary requests. `init` is first-time setup, `list_namespaces` is only for an unknown path, and `recall` is only for a history-dependent decision.

The bundled Codex and Claude Code plugin gives the agent one short memory reminder at a new session startup without calling Stash. Its `UserPromptSubmit` hook sends the prompt to `queue_prompt_history`, which stores it under `/self/history` and returns after the database insert. Embedding runs in the server worker, and configured consolidation processes it later, so prompt handling does not wait for either provider call. The authenticated MCP hook is still one short request per submitted prompt; it reuses the client's configured `stash` MCP connection instead of introducing another credential.

This flow is entirely Web MCP based and does not require a local path, Git repository, or MCP Roots. For code projects, `stash workspace facts`, `resolve_workspace`, `resume_workspace`, and `claim_workspace` remain optional Git connector helpers.

Use `attach_work_resource` to add a short Jira, Confluence, Git, browser, document, API, data, or device reference to the relevant work item. Keep external bodies and credentials in their original systems.

## 4. Background consolidation

When started with `stash serve` (the Docker Compose default), Stash consolidates the configured non-root namespaces in the background. The root namespace `/` is rejected so unrelated session records are not mixed. You can also trigger a selected project manually via the `consolidate` MCP tool or CLI:

```bash
docker compose exec stash /stash consolidate run --namespaces /projects/myapp
```

Each result reports `pending_stage_inputs` and `errors`. The same latest-run values are exported through Prometheus metrics so a growing backlog or repeated pipeline error is visible.

## 5. Configuration checklist

If tools fail, check `.env`:

| Variable | Purpose |
|----------|---------|
| `STASH_OPENAI_API_KEY` | Embeddings + reasoner; optional for endpoints without authentication |
| `STASH_OPENAI_BASE_URL` | API base URL. Every `STASH_OPENAI_*` and model variable is optional: providers can instead be registered in **Model settings** (`/ui/llm`) or with `stash llm`, per feature, and `stash llm import-env` copies these variables into that registry |
| `STASH_SECRETS_KEY` | 64 hex characters (`openssl rand -hex 32`) that seal API keys stored in the database; without it only key-less providers can be registered |
| `STASH_SECRETS_KEY_PREVIOUS` | Comma-separated older secrets keys kept readable during a rotation |
| `STASH_EMBEDDING_CACHE` | Cache computed vectors in PostgreSQL (default `true`) |
| `STASH_OPENAI_REQUEST_TIMEOUT` | Maximum time for one provider request attempt (default `2m`) |
| `STASH_EMBEDDING_MODEL` | Optional. Must match `STASH_VECTOR_DIM` (1536 for `text-embedding-3-small`); without any embedding provider Stash stores memories and searches them by keyword until one is assigned |
| `STASH_VECTOR_DIM` | Output dimension of the embedding model; changing it (or the model, here or in Model settings) automatically queues a full reindex |
| `STASH_EMBEDDING_RETRY_INTERVAL` | How often pending embeddings are retried (default `1m`) |
| `STASH_EMBEDDING_RETRY_MAX_INTERVAL` | Maximum exponential backoff (default `1h`) |
| `STASH_EMBEDDING_RETRY_BATCH_SIZE` | Maximum pending rows considered per pass (default `100`) |
| `STASH_EMBEDDING_CONTEXT_TOKENS` | Embedding model input window; `0` uses adaptive splitting after a provider context error |
| `STASH_ADMIN_USER` | Username of the first administrator; created at startup when missing, otherwise promoted and re-enabled |
| `STASH_ADMIN_PASSWORD` | Password for that first administrator (8–72 characters); not applied to an existing user that already has one |
| `STASH_ADMIN_SUBJECTS` | Comma-separated subjects allowed on the server settings pages next to `is_admin` users |
| `STASH_ADMIN_TOKEN` | Optional separate token for the admin endpoints (`X-Stash-Admin-Token`) |
| `STASH_REASONER_MODEL` | Optional. Model used for consolidation, `validate_work_plan`, and `wiki_compile` unless Model settings assigns another provider per feature |
| `STASH_REASONER_CONTEXT_TOKENS` | Full reasoning-model context window; `0` uses adaptive splitting after a provider context error |
| `STASH_REASONER_RESERVED_TOKENS` | Tokens kept for instructions and the JSON answer (default `4096`) |
| `STASH_CONSOLIDATE_NAMESPACES` | Non-root namespaces processed by Docker Compose background consolidation (default `/projects`) |
| `STASH_MCP_MAX_RESPONSE_BYTES` | Maximum JSON bytes in one MCP tool result (default `32768`); large pages return `next_offset` |
| `STASH_MCP_TOOL_TIMEOUT` | Maximum time for one MCP tool call (default `2m`) |
| `STASH_AUTH_MODE` | `none`, `token`, `oauth` (same as `token`; kept for existing deployments), or `stdio` |
| `STASH_AUTH_ISSUER`, `STASH_AUTH_CLIENT_ID`, `STASH_AUTH_CLIENT_SECRET`, `STASH_AUTH_REDIRECT_URL` | The first SSO (OIDC) provider; imported into the `sso_providers` table at startup and managed in the console afterwards |
| `STASH_AUTH_TRUSTED_NETWORK` | `true` lets `STASH_AUTH_MODE=none` bind beyond loopback on a network you trust (logs a warning) |
| `STASH_AUTH_API_SECRET` | At least 32 random bytes used to sign Stash tokens and sessions |
| `STASH_AUTH_TOKEN_TTL` | Default lifetime of tokens issued by `stash mcp token` (default `720h`; `--ttl 0` means no expiry) |
| `STASH_AUTH_SESSION_TTL` | Browser console session lifetime after login; renewed while in use (default `720h`) |
| `STASH_AUTH_COOKIE_SECURE` | `false` for loopback HTTP; `true` for public HTTPS |

### HTTP MCP authentication

MCP clients authenticate with a Stash API token in every profile. The token
is stored in the database (only a digest), is listed and revocable in the
console, and does not change until it expires or is revoked, so an agent keeps
the same subject and the same history across sessions. Issue one in the
console (**API tokens**) or on the server host:

```bash
stash mcp token --subject agent-1 --name "build box"   # --ttl 0 for no expiry
```

Send the result as `Authorization: Bearer <stash_api_token>`. Nothing else is
accepted on `/mcp`: Stash no longer brokers OAuth for MCP clients, and an
identity provider's access token is not a Stash credential.

`STASH_AUTH_MODE=oauth` only changes how people reach the console: it adds SSO
login through the configured OIDC provider next to the password form. For a
local CLI process, use `STASH_AUTH_MODE=stdio`, optionally with a Stash API
token in `STASH_AUTH_STDIO_TOKEN`. `STASH_AUTH_MODE=none` disables HTTP
authentication and is accepted only on a loopback address (or with
`STASH_AUTH_TRUSTED_NETWORK=true`).

### Console login and users

With `STASH_AUTH_MODE=token` or `oauth`, people sign in to the console with a
username and password. Set `STASH_ADMIN_USER` and `STASH_ADMIN_PASSWORD` for
the first administrator; it is created at the first start. Then manage users
on the server host:

```bash
stash user add alice --password-stdin --display-name "Alice"
stash user set alice --admin        # or --no-admin, --disable, --enable
stash user passwd alice --password-stdin
stash user remove alice             # revokes its API tokens; memory stays
```

The same operations are on the console's **Users & SSO** page, which also
lists each person's API tokens by creation date and revokes them.
A user is a person; `stash user list` also shows how each one signs in
(a password, an SSO subject, or both). SSO logins are matched to users by
issuer and subject and provisioned on first login. The login page shows the
password form first; `/auth/login?provider=token` keeps the API-token form
for a client that only has a token.

SSO providers live in the database. Set `STASH_AUTH_ISSUER`,
`STASH_AUTH_CLIENT_ID`, `STASH_AUTH_CLIENT_SECRET`, and
`STASH_AUTH_REDIRECT_URL` once to register the first one; the server imports
it at startup when `STASH_SECRETS_KEY` is set (the secret is sealed with it).
Add, test, disable, or remove providers on the console's **Users & SSO** page
or with `stash sso list|add|set|test|remove`. Register
`https://<stash>/auth/callback` as the redirect URL at the identity provider.

### Embedding maintenance

The web console exposes the **Server settings** pages (Model settings and
Embedding maintenance) to administrators: `is_admin` users, subjects in
`STASH_ADMIN_SUBJECTS`, or requests with `STASH_ADMIN_TOKEN`. It shows pending
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
- A `401` response means the client did not send a valid bearer token generated in step 2. Confirm that the Codex process inherited `STASH_MCP_TOKEN` and that `codex mcp get stash` names the same environment variable.
- OAuth discovery `404` responses mean the client tried an OAuth login flow that Stash does not offer. Remove the MCP entry, add it again with `--bearer-token-env-var STASH_MCP_TOKEN`, and do not run `codex mcp login stash`.
- A `401` after an upgrade means the client still sends an OAuth access token from the old flow. Issue a Stash API token and configure the client with it.

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
