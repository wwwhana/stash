#!/usr/bin/env python3
"""Track successful Stash leases and gate Codex or Claude's Stop event."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
from typing import Any


START_TOOLS = {"claim_work", "start_work", "claim_workspace"}
END_TOOLS = {"finish_work", "handoff_work"}


def state_path(event: dict[str, Any]) -> Path | None:
    session_id = str(event.get("session_id", "")).strip()
    if not session_id:
        return None
    root = os.environ.get("PLUGIN_DATA") or os.environ.get("CLAUDE_PLUGIN_DATA")
    if not root:
        root = os.path.join(tempfile.gettempdir(), "stash-work-plan")
    directory = Path(root).expanduser()
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    digest = hashlib.sha256(session_id.encode("utf-8")).hexdigest()
    return directory / f"{digest}.json"


def write_active(path: Path) -> None:
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    temporary.write_text('{"active":true}\n', encoding="utf-8")
    os.chmod(temporary, 0o600)
    os.replace(temporary, path)


def clear_active(path: Path) -> None:
    try:
        path.unlink()
    except FileNotFoundError:
        pass


def response_succeeded(event: dict[str, Any]) -> bool:
    if "tool_response" not in event:
        return False
    response = event["tool_response"]
    if isinstance(response, str):
        try:
            response = json.loads(response)
        except json.JSONDecodeError:
            return bool(response.strip())
    if not isinstance(response, dict):
        return response is not None
    if (
        response.get("isError") is True
        or response.get("is_error") is True
        or response.get("error") is not None
    ):
        return False
    result = response.get("result")
    if isinstance(result, dict) and (
        result.get("isError") is True
        or result.get("is_error") is True
        or result.get("error") is not None
    ):
        return False
    return True


def handle_tool(event: dict[str, Any]) -> None:
    path = state_path(event)
    if path is None or not response_succeeded(event):
        return
    tool_name = str(event.get("tool_name", ""))
    tool = tool_name.rsplit("__", 1)[-1]
    if tool in START_TOOLS:
        write_active(path)
    elif tool in END_TOOLS:
        clear_active(path)


def handle_stop(event: dict[str, Any]) -> None:
    path = state_path(event)
    if path is None or not path.exists():
        json.dump({}, sys.stdout)
        sys.stdout.write("\n")
        return
    json.dump(
        {
            "decision": "block",
            "reason": (
                "Stash 작업을 끝내려면 성공한 finish_work 또는 handoff_work 호출이 먼저 필요합니다. "
                "완료 조건을 확인했으면 finish_work, 남은 작업이 있으면 handoff_work를 호출하세요."
            ),
        },
        sys.stdout,
        ensure_ascii=False,
    )
    sys.stdout.write("\n")


def main() -> int:
    try:
        event = json.load(sys.stdin)
    except (json.JSONDecodeError, TypeError):
        return 0
    if not isinstance(event, dict):
        return 0
    event_name = event.get("hook_event_name")
    if event_name == "PostToolUse":
        handle_tool(event)
    elif event_name == "Stop":
        handle_stop(event)
    elif event_name == "SessionEnd":
        path = state_path(event)
        if path is not None:
            clear_active(path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
