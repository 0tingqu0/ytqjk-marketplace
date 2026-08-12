from __future__ import annotations

import argparse
import hashlib
import json
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

from platform_paths import default_knowledge_root
from rag_security import contains_high_confidence_secret


MAX_MEMORY_CHARS = 24_000


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def session_key(session_id: str) -> str:
    return hashlib.sha256(session_id.encode("utf-8")).hexdigest()[:24]


def anchor_path(root: Path, session_id: str) -> Path:
    return root / "sessions" / session_key(session_id) / "anchor.json"


def read_anchor(root: Path, session_id: str) -> dict[str, object]:
    path = anchor_path(root, session_id)
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def ensure_anchor(root: Path, session_id: str, project_id: str) -> tuple[dict[str, object], bool]:
    validate_session_id(session_id)
    existing = read_anchor(root, session_id)
    now = utc_now()
    anchor = {
        "schema_version": 1,
        "session_key": session_key(session_id),
        "project_id": project_id,
        "created_at": existing.get("created_at", now),
        "last_activity_at": now,
        "archived_at": None,
        "memory": existing.get("memory", ""),
        "exported_memory_hash": existing.get("exported_memory_hash", ""),
    }
    path = anchor_path(root, session_id)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(anchor, ensure_ascii=False, indent=2), encoding="utf-8")
    return anchor, not bool(existing)


def write_anchor(root: Path, session_id: str, project_id: str) -> dict[str, object]:
    anchor, _ = ensure_anchor(root, session_id, project_id)
    return anchor


def checkpoint(root: Path, session_id: str, project_id: str, memory: str) -> dict[str, object]:
    validate_memory(memory)
    anchor = write_anchor(root, session_id, project_id)
    anchor["memory"] = memory.strip()
    anchor["last_activity_at"] = utc_now()
    anchor_path(root, session_id).write_text(
        json.dumps(anchor, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    return anchor


def restore(root: Path, session_id: str) -> dict[str, object]:
    anchor = read_anchor(root, session_id)
    if not anchor:
        raise ValueError("未找到会话锚点。")
    anchor["last_activity_at"] = utc_now()
    anchor_path(root, session_id).write_text(
        json.dumps(anchor, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    return anchor


def archive(root: Path, session_id: str) -> dict[str, object]:
    anchor = read_anchor(root, session_id)
    if not anchor:
        raise ValueError("未找到会话锚点。")
    if anchor.get("archived_at"):
        return anchor
    memory = str(anchor.get("memory", "")).strip()
    memory_hash = memory_digest(memory)
    if memory and anchor.get("exported_memory_hash") != memory_hash:
        write_experience(root, anchor, memory)
        anchor["exported_memory_hash"] = memory_hash
    anchor["archived_at"] = utc_now()
    anchor_path(root, session_id).write_text(
        json.dumps(anchor, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    return anchor


def sweep(root: Path, days: int) -> list[str]:
    cutoff = datetime.now(timezone.utc) - timedelta(days=days)
    archived = []
    for path in (root / "sessions").glob("*/anchor.json"):
        try:
            anchor = json.loads(path.read_text(encoding="utf-8"))
            last_activity = datetime.fromisoformat(str(anchor["last_activity_at"]))
        except (OSError, KeyError, ValueError, json.JSONDecodeError):
            continue
        if anchor.get("archived_at") or last_activity > cutoff:
            continue
        memory = str(anchor.get("memory", "")).strip()
        memory_hash = memory_digest(memory)
        if memory and anchor.get("exported_memory_hash") != memory_hash:
            write_experience(root, anchor, memory)
            anchor["exported_memory_hash"] = memory_hash
        anchor["archived_at"] = utc_now()
        path.write_text(json.dumps(anchor, ensure_ascii=False, indent=2), encoding="utf-8")
        archived.append(str(anchor["session_key"]))
    return archived


def write_experience(root: Path, anchor: dict[str, object], memory: str) -> Path:
    validate_memory(memory)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    target = root / "personal-experience/candidates" / f"{stamp}-session-{anchor['session_key']}.md"
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(
        "---\nstatus: CANDIDATE\nsource: session-anchor\n"
        f"session_key: {anchor['session_key']}\nproject_id: {anchor['project_id']}\n"
        f"archived_at: {utc_now()}\n---\n\n# 会话经验\n\n{memory}\n",
        encoding="utf-8",
    )
    return target


def validate_memory(memory: str) -> None:
    if not memory.strip() or len(memory) > MAX_MEMORY_CHARS:
        raise ValueError("会话摘要必须非空且不超过 24000 字符。")
    if "\x00" in memory or contains_high_confidence_secret(memory):
        raise ValueError("会话摘要可能包含敏感信息，未保存。")


def memory_digest(memory: str) -> str:
    return hashlib.sha256(memory.encode("utf-8")).hexdigest() if memory else ""


def validate_session_id(session_id: str) -> None:
    if not session_id.strip() or len(session_id) > 512 or "\x00" in session_id:
        raise ValueError("会话标识无效。")


def main() -> None:
    parser = argparse.ArgumentParser(description="YTQJK session-anchor memory store.")
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    sub = parser.add_subparsers(dest="command", required=True)
    for name in ("anchor", "checkpoint", "restore", "archive"):
        item = sub.add_parser(name)
        item.add_argument("--session-id", required=True)
        if name in {"anchor", "checkpoint"}:
            item.add_argument("--project-id", required=True)
        if name == "checkpoint":
            item.add_argument("--memory-file", type=Path, required=True)
    sweep_parser = sub.add_parser("sweep")
    sweep_parser.add_argument("--days", type=int, default=30)
    args = parser.parse_args()
    root = args.knowledge_root.resolve()
    if args.command == "anchor":
        result = write_anchor(root, args.session_id, args.project_id)
    elif args.command == "checkpoint":
        result = checkpoint(root, args.session_id, args.project_id, args.memory_file.read_text(encoding="utf-8"))
    elif args.command == "restore":
        result = restore(root, args.session_id)
    elif args.command == "archive":
        result = archive(root, args.session_id)
    else:
        result = {"archived": sweep(root, args.days)}
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
