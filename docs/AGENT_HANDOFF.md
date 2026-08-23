# Seamless Agent Handoff (Auto-Save & Load)

Stash provides the memory backend, but for a truly seamless experience, your AI agent needs to *use* it automatically. Instead of manually telling the agent to "save" or "load" every time, you can configure your MCP client to enforce this behavior.

There are two approaches: **Prompt-based Rules** (works everywhere) and **Lifecycle Hooks** (requires specific IDEs like Antigravity).

---

## 1. Prompt-based Rules (Cursor, Windsurf, Claude Desktop)

The easiest way to enforce state synchronization is to add a strict rule to your workspace's system prompt (e.g., `.cursorrules`, `.windsurfrules`, or Agent instructions).

Create a `.cursorrules` file in your project root:

```markdown
# Mandatory Stash Synchronization
You are connected to the `stash` MCP server for persistent memory. You must maintain state continuity across sessions.

1. **Auto-Load**: At the start of a new task or conversation, ALWAYS call `get_context` (namespace: your project path) to understand the current focus, and `list_failures` to know what approaches to avoid.
2. **Auto-Save**: BEFORE you end your turn, finish a task, or provide your final answer to the user, you MUST ALWAYS call `set_context` to save your current progress, what you just did, and what the `next_steps` are.
3. DO NOT STOP or conclude a conversation without saving your state to Stash.
```

By adding this, the LLM is heavily biased towards synchronizing its state automatically.

---

## 2. Advanced Automation with Lifecycle Hooks (Antigravity IDE)

If you use an orchestrator or IDE that supports lifecycle hooks (like **Antigravity**), you can completely remove the reliance on the LLM's memory by forcing the system to load and save mechanically.

### Auto-Load (PreInvocation Hook)
Inject the context before the agent even sees the prompt.

Create `.agents/hooks.json`:
```json
{
  "stash-auto-hydrate": {
    "PreInvocation": [
      {
        "type": "command",
        "command": "./.agents/scripts/auto_load_context.sh"
      }
    ]
  }
}
```

Create `.agents/scripts/auto_load_context.sh`:
```bash
#!/bin/bash
INVOCATION_NUM=$(jq -r '.invocationNum' < /dev/stdin)

if [ "$INVOCATION_NUM" -eq 1 ]; then
  # Fetch context from stash (assuming streamable HTTP at /mcp)
  # Replace with actual curl/cli command to your stash instance
  CONTEXT=$(stash mcp execute --tool get_context --namespace /projects/my-project)
  
  jq -n --arg ctx "$CONTEXT" '{
    injectSteps: [
      { ephemeralMessage: ("Here is the current stash context from previous agent:\n" + $ctx) }
    ]
  }'
else
  echo "{}"
fi
```

### Enforce Auto-Save (Stop Hook)
Prevent the agent from exiting if it forgot to call `set_context`.

Add to `.agents/hooks.json`:
```json
{
  "stash-safety-gate": {
    "Stop": [
      {
        "type": "command",
        "command": "./.agents/scripts/prevent_exit.sh"
      }
    ]
  }
}
```

Create `.agents/scripts/prevent_exit.sh`:
```bash
#!/bin/bash
TRANSCRIPT_PATH=$(jq -r '.transcriptPath' < /dev/stdin)

if ! grep -q '"tool_name":"set_context"' "$TRANSCRIPT_PATH"; then
  jq -n '{
    decision: "continue",
    reason: "CRITICAL: You forgot to call `set_context` to save your state. You MUST save your state before stopping."
  }'
else
  jq -n '{ decision: "stop" }'
fi
```

With these hooks, your workspace becomes a perfectly stateful environment where any agent session can pick up exactly where the last one left off.
