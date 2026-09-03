#!/usr/bin/env python3

import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest


HOOK = Path(__file__).with_name("stash_work_hook.py")
HOOKS = Path(__file__).with_name("hooks.json")


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
                    "tool_response": {
                        "structuredContent": {
                            "status": "handed_off",
                            "result_memory_linked": True,
                        }
                    },
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
                    "tool_response": {
                        "content": [
                            {
                                "type": "text",
                                "text": json.dumps(
                                    {"status": "finished", "result_memory_linked": True}
                                ),
                            }
                        ]
                    },
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

    def test_terminal_response_without_result_memory_keeps_session_blocked(self):
        with tempfile.TemporaryDirectory() as directory:
            plugin_data = Path(directory)
            base = {"session_id": "session-missing-memory"}
            self.run_hook(
                {
                    **base,
                    "hook_event_name": "PostToolUse",
                    "tool_name": "mcp__stash__claim_work",
                    "tool_response": {"ok": True},
                },
                plugin_data,
            )
            env = {**os.environ, "PLUGIN_DATA": str(plugin_data), "CLAUDE_PLUGIN_DATA": ""}
            result = subprocess.run(
                [sys.executable, str(HOOK)],
                input=json.dumps(
                    {
                        **base,
                        "hook_event_name": "PostToolUse",
                        "tool_name": "mcp__stash__finish_work",
                        "tool_response": {"structuredContent": {"status": "completed"}},
                    }
                ),
                text=True,
                capture_output=True,
                check=False,
                env=env,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("result_memory_linked=true", result.stderr)
            blocked = self.run_hook({**base, "hook_event_name": "Stop"}, plugin_data)
            self.assertEqual(json.loads(blocked.stdout)["decision"], "block")

    def test_invalid_hook_input_is_visible(self):
        result = subprocess.run(
            [sys.executable, str(HOOK)],
            input="not-json",
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("입력을 읽지 못했습니다", result.stderr)

        missing_session = subprocess.run(
            [sys.executable, str(HOOK)],
            input=json.dumps({"hook_event_name": "Stop"}),
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertNotEqual(missing_session.returncode, 0)
        self.assertIn("session_id", missing_session.stderr)

    def test_session_start_only_injects_one_memory_reminder(self):
        config = json.loads(HOOKS.read_text(encoding="utf-8"))
        session_group = config["hooks"]["SessionStart"][0]
        self.assertEqual(session_group["matcher"], "startup")
        session_hooks = session_group["hooks"]
        self.assertEqual(len(session_hooks), 1)
        self.assertEqual(session_hooks[0]["type"], "command")
        self.assertNotIn("server", session_hooks[0])
        self.assertNotIn("tool", session_hooks[0])

        with tempfile.TemporaryDirectory() as directory:
            result = self.run_hook(
                {
                    "hook_event_name": "SessionStart",
                    "session_id": "session-start",
                    "source": "startup",
                },
                Path(directory),
            )
        output = json.loads(result.stdout)["hookSpecificOutput"]
        self.assertEqual(output["hookEventName"], "SessionStart")
        self.assertIn("프롬프트는 자동으로 저장 대기열에 들어갑니다", output["additionalContext"])
        self.assertIn("저장되지 않은 것", output["additionalContext"])
        self.assertIn("결정·정정·실패·교훈", output["additionalContext"])

    def test_user_prompt_is_queued_through_the_authenticated_mcp_connection(self):
        config = json.loads(HOOKS.read_text(encoding="utf-8"))
        prompt_groups = config["hooks"]["UserPromptSubmit"]
        self.assertEqual(len(prompt_groups), 1)
        prompt_hooks = prompt_groups[0]["hooks"]
        self.assertEqual(len(prompt_hooks), 1)
        hook = prompt_hooks[0]
        self.assertEqual(hook["type"], "mcp_tool")
        self.assertEqual((hook["server"], hook["tool"]), ("stash", "queue_prompt_history"))
        self.assertEqual(hook["input"], {"prompt": "${prompt}"})
        self.assertEqual(hook["timeout"], 3)
        self.assertNotIn("async", hook)

    def test_work_state_hook_ignores_unrelated_stash_tools(self):
        config = json.loads(HOOKS.read_text(encoding="utf-8"))
        matcher = config["hooks"]["PostToolUse"][0]["matcher"]

        for tool in (
            "mcp__stash__claim_work",
            "mcp__stash-local__start_work",
            "mcp__stash__claim_workspace",
            "mcp__stash__finish_work",
            "mcp__stash__handoff_work",
        ):
            self.assertIsNotNone(re.fullmatch(matcher, tool), tool)
        for tool in (
            "mcp__stash__recall",
            "mcp__stash__remember",
            "mcp__stash__queue_prompt_history",
            "mcp__stash__checkpoint_work",
        ):
            self.assertIsNone(re.fullmatch(matcher, tool), tool)


if __name__ == "__main__":
    unittest.main()
