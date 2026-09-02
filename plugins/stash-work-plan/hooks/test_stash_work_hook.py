#!/usr/bin/env python3

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


HOOK = Path(__file__).with_name("stash_work_hook.py")


class StashWorkHookTest(unittest.TestCase):
    def run_hook(self, data, plugin_data, data_env="PLUGIN_DATA"):
        env = {**os.environ, "PLUGIN_DATA": "", "CLAUDE_PLUGIN_DATA": ""}
        env[data_env] = str(plugin_data)
        return subprocess.run(
            [sys.executable, str(HOOK)],
            input=json.dumps(data),
            text=True,
            capture_output=True,
            check=True,
            env=env,
        )

    def test_claim_requires_successful_finish_or_handoff(self):
        with tempfile.TemporaryDirectory() as directory:
            plugin_data = Path(directory)
            base = {"session_id": "session-1"}

            self.run_hook(
                {
                    **base,
                    "hook_event_name": "PostToolUse",
                    "tool_name": "mcp__stash__claim_work",
                    "tool_response": {"structuredContent": {"attempt_id": 7}},
                },
                plugin_data,
            )
            blocked = self.run_hook({**base, "hook_event_name": "Stop"}, plugin_data)
            self.assertEqual(json.loads(blocked.stdout)["decision"], "block")

            self.run_hook(
                {
                    **base,
                    "hook_event_name": "PostToolUse",
                    "tool_name": "mcp__stash__finish_work",
                    "tool_response": {"is_error": True, "error": "conditions remain"},
                },
                plugin_data,
            )
            still_blocked = self.run_hook({**base, "hook_event_name": "Stop"}, plugin_data)
            self.assertEqual(json.loads(still_blocked.stdout)["decision"], "block")

            self.run_hook(
                {
                    **base,
                    "hook_event_name": "PostToolUse",
                    "tool_name": "mcp__stash__handoff_work",
                    "tool_response": {"structuredContent": {"status": "handed_off"}},
                },
                plugin_data,
            )
            allowed = self.run_hook({**base, "hook_event_name": "Stop"}, plugin_data)
            self.assertEqual(json.loads(allowed.stdout), {})

            self.run_hook(
                {
                    **base,
                    "hook_event_name": "PostToolUse",
                    "tool_name": "mcp__stash__claim_work",
                    "tool_response": {"structuredContent": {"attempt_id": 8}},
                },
                plugin_data,
            )
            self.run_hook(
                {
                    **base,
                    "hook_event_name": "PostToolUse",
                    "tool_name": "mcp__stash__finish_work",
                    "tool_response": {"structuredContent": {"status": "finished"}},
                },
                plugin_data,
            )
            allowed_after_finish = self.run_hook({**base, "hook_event_name": "Stop"}, plugin_data)
            self.assertEqual(json.loads(allowed_after_finish.stdout), {})

    def test_untracked_session_is_allowed_and_session_end_cleans_state(self):
        with tempfile.TemporaryDirectory() as directory:
            plugin_data = Path(directory)
            base = {"session_id": "session-2"}
            claude_data_env = "CLAUDE_PLUGIN_DATA"
            allowed = self.run_hook({**base, "hook_event_name": "Stop"}, plugin_data, claude_data_env)
            self.assertEqual(json.loads(allowed.stdout), {})

            self.run_hook(
                {
                    **base,
                    "hook_event_name": "PostToolUse",
                    "tool_name": "mcp__stash-local__start_work",
                    "tool_response": {"ok": True},
                },
                plugin_data,
                claude_data_env,
            )
            blocked = self.run_hook({**base, "hook_event_name": "Stop"}, plugin_data, claude_data_env)
            self.assertEqual(json.loads(blocked.stdout)["decision"], "block")
            self.run_hook({**base, "hook_event_name": "SessionEnd"}, plugin_data, claude_data_env)
            allowed_after_end = self.run_hook({**base, "hook_event_name": "Stop"}, plugin_data, claude_data_env)
            self.assertEqual(json.loads(allowed_after_end.stdout), {})


if __name__ == "__main__":
    unittest.main()
