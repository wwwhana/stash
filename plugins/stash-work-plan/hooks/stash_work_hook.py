#!/usr/bin/env python3
"""Inject one memory reminder and enforce successful Stash work endings."""

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
SESSION_MEMORY_GUIDANCE = (
    "Stash 기억 안내: 사용자가 보낸 프롬프트는 자동으로 저장 대기열에 들어갑니다. "
    "오류가 표시되면 해당 프롬프트는 저장되지 않은 것입니다. "
    "저장된 기억이 현재 판단을 바꿀 때만 recall을 사용하고, "
    "결정·정정·실패·교훈만 remember 또는 remember_work로 남기세요."
)


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


def response_has_result_memory_link(value: Any) -> bool:
    if isinstance(value, str):
        try:
            value = json.loads(value)
        except json.JSONDecodeError:
            return False
    if isinstance(value, list):
        return any(response_has_result_memory_link(item) for item in value)
    if not isinstance(value, dict):
        return False
    if value.get("result_memory_linked") is True:
        return True
    return any(
        response_has_result_memory_link(value.get(key))
        for key in ("structuredContent", "structured_content", "result", "content", "text")
        if key in value
    )


def handle_tool(event: dict[str, Any]) -> bool:
    tool_name = str(event.get("tool_name", ""))
    tool = tool_name.rsplit("__", 1)[-1]
    if tool not in START_TOOLS and tool not in END_TOOLS:
        return True
    path = state_path(event)
    if path is None:
        print("Stash 훅 입력에 session_id가 없습니다.", file=sys.stderr)
        return False
    if not response_succeeded(event):
        return True
    if tool in START_TOOLS:
        write_active(path)
    elif tool in END_TOOLS:
        if not response_has_result_memory_link(event.get("tool_response")):
            print(
                f"Stash {tool} 응답에서 result_memory_linked=true를 확인하지 못했습니다.",
                file=sys.stderr,
            )
            return False
        clear_active(path)
    return True


def handle_stop(event: dict[str, Any]) -> bool:
    path = state_path(event)
    if path is None:
        print("Stash 훅 입력에 session_id가 없습니다.", file=sys.stderr)
        return False
    if not path.exists():
        json.dump({}, sys.stdout)
        sys.stdout.write("\n")
        return True
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
    return True


def handle_session_start() -> None:
    json.dump(
        {
            "hookSpecificOutput": {
                "hookEventName": "SessionStart",
                "additionalContext": SESSION_MEMORY_GUIDANCE,
            }
        },
        sys.stdout,
        ensure_ascii=False,
    )
    sys.stdout.write("\n")


def main() -> int:
    try:
        event = json.load(sys.stdin)
    except (json.JSONDecodeError, TypeError) as exc:
        print(f"Stash 훅 입력을 읽지 못했습니다: {exc}", file=sys.stderr)
        return 1
    if not isinstance(event, dict):
        print("Stash 훅 입력은 JSON 객체여야 합니다.", file=sys.stderr)
        return 1
    event_name = event.get("hook_event_name")
    if event_name == "SessionStart":
        handle_session_start()
    elif event_name == "PostToolUse":
        if not handle_tool(event):
            return 1
    elif event_name == "Stop":
        if not handle_stop(event):
            return 1
    elif event_name == "SessionEnd":
        path = state_path(event)
        if path is not None:
            clear_active(path)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
