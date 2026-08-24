# Stash

[🇺🇸 English](README.md) | [🇰🇷 한국어](README.ko.md)

> **Note:** This is an enhanced fork of the original [alash3al/stash](https://github.com/alash3al/stash). 
> This version (`wwwhana/stash`) introduces significant improvements including Hybrid Search (Vector + Trigram RRF), deterministic episode clustering, safer soft-delete lifecycle, and advanced MCP auto-hydration hooks.
>
>
> Learn more orignal
>
> [**alash3al.github.io/stash →**](https://alash3al.github.io/stash/)

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

That's it. Postgres + pgvector, migrations, MCP server with background consolidation — all in one command.

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
**Important Tuning Note:** If you are using `multilingual-e5-small` or similar models, make sure you match the `STASH_VECTOR_DIM` to the model's output dimensions (e.g., `384`).

```bash
STASH_OPENAI_BASE_URL=http://host.docker.internal:11434/v1
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

For a remote, OIDC-protected MCP server, use Streamable HTTP and OAuth:

```bash
codex mcp add stash --url https://stash.example.com/mcp --oauth-client-id stash-codex
codex mcp login stash
```

Set `STASH_AUTH_MODE=oidc`, the issuer/client settings, and
`STASH_AUTH_MCP_CLIENT_ID` for the OAuth client used by Codex.

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

### Auto-Save &amp; Seamless Handoff

To make your AI agents automatically save their memory and pass the baton between sessions without manual prompting, read the [**Seamless Agent Handoff Guide**](docs/AGENT_HANDOFF.md). You can configure Cursor, Claude Desktop, or Antigravity IDE to mechanically load and save context.

## What It Does

Stash is a cognitive layer between your AI agent and the world. Episodes become facts. Facts become relationships. Relationships become patterns. Patterns become wisdom.

A 9-stage consolidation pipeline turns raw observations into structured knowledge — facts, relationships, causal links, patterns, contradictions, goal tracking, failure patterns, and hypothesis verification. Each stage only processes new data since the last run.

## License

Apache 2.0
