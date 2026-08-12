from __future__ import annotations

import json
import re
from datetime import datetime, timezone
from pathlib import Path

from rag_security import contains_high_confidence_secret
from session_memory import archive, checkpoint, read_anchor, session_key


ARCHIVE_NAME = re.compile(r".*-([0-9a-f]{8}-[0-9a-f-]{27})\.jsonl$", re.IGNORECASE)
MAX_SUMMARY_CHARS = 6_000


def archived_session_id(path: Path) -> str | None:
    match = ARCHIVE_NAME.fullmatch(path.name)
    return match.group(1) if match else None


def final_answer(path: Path) -> str:
    result = ""
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError:
        return result
    for line in lines:
        try:
            item = json.loads(line)
            payload = item.get("payload", {})
        except json.JSONDecodeError:
            continue
        if not isinstance(payload, dict) or payload.get("phase") != "final_answer":
            continue
        if payload.get("type") == "agent_message":
            message = payload.get("message")
            if isinstance(message, str):
                result = message
        if payload.get("type") == "message" and payload.get("role") == "assistant":
            content = payload.get("content", [])
            if isinstance(content, list):
                texts = [entry.get("text", "") for entry in content if isinstance(entry, dict)]
                result = "\n".join(text for text in texts if isinstance(text, str))
    return result.strip()[:MAX_SUMMARY_CHARS]


def sync_archived_sessions(root: Path, codex_home: Path | None = None) -> list[str]:
    archive_dir = (codex_home or Path.home() / ".codex") / "archived_sessions"
    if not archive_dir.is_dir():
        return []
    synced = []
    for path in archive_dir.glob("*.jsonl"):
        session_id = archived_session_id(path)
        if not session_id:
            continue
        anchor = read_anchor(root, session_id)
        if not anchor or anchor.get("archived_at"):
            continue
        memory = final_answer(path)
        if not memory or contains_high_confidence_secret(memory):
            continue
        project_id = anchor.get("project_id")
        if not isinstance(project_id, str):
            continue
        checkpoint(root, session_id, project_id, memory)
        archive(root, session_id)
        synced.append(session_key(session_id))
    return synced
