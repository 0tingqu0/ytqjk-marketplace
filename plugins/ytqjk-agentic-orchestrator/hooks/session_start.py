from __future__ import annotations

import json
import sys
from pathlib import Path


PLUGIN_ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = PLUGIN_ROOT / "skills" / "ytqjk" / "scripts"
sys.path.insert(0, str(SCRIPTS))

from platform_paths import default_knowledge_root  # noqa: E402
from project_source import is_git_project  # noqa: E402
from project_tracking import identify_project, track_project  # noqa: E402
from session_memory import ensure_anchor  # noqa: E402


def context(receipt: dict[str, object]) -> str:
    return (
        "KNOWLEDGE_RECEIPT "
        f"status={receipt['status']} project_id={receipt['project_id']} "
        f"project_tracking=REGISTERED scope=session-anchor "
        f"anchor_key={receipt['anchor_key']} "
        f"anchor_created={str(receipt['anchor_created']).lower()}. "
        "Before answering project questions, query YTQJK knowledge through "
        "skills/ytqjk/scripts/session_query.py with this project_id and the "
        "current session_id."
    )


def anchor_session(event: dict[str, object]) -> dict[str, object] | None:
    session_id = str(event.get("session_id") or "").strip()
    cwd = Path(str(event.get("cwd") or "")).resolve()
    if not session_id or not cwd.is_dir() or not is_git_project(cwd):
        return None
    root = default_knowledge_root().resolve()
    project = identify_project(cwd)
    track_project(root, cwd, project)
    anchor, created = ensure_anchor(root, session_id, project["id"])
    return {
        "status": "SESSION_ANCHORED",
        "project_id": project["id"],
        "anchor_key": anchor["session_key"],
        "anchor_created": created,
    }


def main() -> int:
    try:
        event = json.load(sys.stdin)
        receipt = anchor_session(event)
        if receipt is None:
            return 0
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "SessionStart",
                "additionalContext": context(receipt),
            }
        }, ensure_ascii=False))
        return 0
    except (OSError, RuntimeError, ValueError, json.JSONDecodeError) as error:
        print(json.dumps({
            "systemMessage": (
                "YTQJK session anchoring failed: " + type(error).__name__
            )
        }, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
