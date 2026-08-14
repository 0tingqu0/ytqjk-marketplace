from __future__ import annotations

import argparse
import hashlib
import json
import os
import tempfile
from datetime import datetime, timedelta, timezone
from pathlib import Path

from file_lock import exclusive_file_lock
from platform_paths import default_knowledge_root
from project_tracking import require_tracked_project
from rag_locks import maintenance_lock
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
    if not path.exists():
        return {}
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise ValueError("会话锚点已损坏，已拒绝覆盖。") from exc


def write_anchor_file(path: Path, anchor: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(
        dir=path.parent, prefix=f"{path.name}.", suffix=".tmp"
    )
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(anchor, handle, ensure_ascii=False, indent=2)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def ensure_active(
    anchor: dict[str, object], *, allow_prepared: bool = False
) -> None:
    if anchor.get("archived_at"):
        raise ValueError("会话已归档，不能重新锚定或恢复。")
    if anchor.get("archive_prepared_at") and not allow_prepared:
        raise ValueError("会话正在等待归档完成，不能重新锚定或恢复。")


def _ensure_anchor(
    root: Path,
    session_id: str,
    project_id: str,
    *,
    allow_prepared: bool = False,
) -> tuple[dict[str, object], bool]:
    existing = read_anchor(root, session_id)
    bound_project = existing.get("project_id")
    if existing and bound_project != project_id:
        raise ValueError(f"会话已绑定项目 {bound_project}，禁止访问其他项目子库。")
    if existing:
        ensure_active(existing, allow_prepared=allow_prepared)
    now = utc_now()
    anchor = {
        "schema_version": 1,
        "session_key": session_key(session_id),
        "project_id": project_id,
        "created_at": existing.get("created_at", now),
        "last_activity_at": now,
        "archived_at": None,
        "archive_prepared_at": existing.get("archive_prepared_at"),
        "memory": existing.get("memory", ""),
        "exported_memory_hash": existing.get("exported_memory_hash", ""),
    }
    write_anchor_file(anchor_path(root, session_id), anchor)
    return anchor, not bool(existing)


def ensure_anchor(
    root: Path,
    session_id: str,
    project_id: str,
) -> tuple[dict[str, object], bool]:
    validate_session_id(session_id)
    path = anchor_path(root, session_id)
    with exclusive_file_lock(maintenance_lock(root)):
        require_tracked_project(root, project_id)
        with exclusive_file_lock(path.with_suffix(".lock")):
            return _ensure_anchor(root, session_id, project_id)


def inspect_anchor(
    root: Path, session_id: str, project_id: str
) -> dict[str, object]:
    validate_session_id(session_id)
    path = anchor_path(root, session_id)
    with exclusive_file_lock(path.with_suffix(".lock")):
        anchor = read_anchor(root, session_id)
        if not anchor:
            return {"state": "ABSENT", "session_key": session_key(session_id)}
        if anchor.get("project_id") != project_id:
            raise ValueError(
                f"会话已绑定项目 {anchor.get('project_id')}，禁止作为当前项目会话复用。"
            )
        return {
            "state": _anchor_state(anchor),
            "session_key": anchor["session_key"],
            "project_id": anchor["project_id"],
            "has_memory": bool(str(anchor.get("memory", "")).strip()),
        }


def validate_session_binding(
    root: Path,
    session_id: str,
    project_id: str,
) -> None:
    validate_session_id(session_id)
    path = anchor_path(root, session_id)
    with exclusive_file_lock(path.with_suffix(".lock")):
        anchor = read_anchor(root, session_id)
        if anchor and anchor.get("project_id") != project_id:
            raise ValueError("会话已绑定其他项目，禁止访问其他项目子库。")


def write_anchor(
    root: Path,
    session_id: str,
    project_id: str,
) -> dict[str, object]:
    anchor, _ = ensure_anchor(root, session_id, project_id)
    return anchor


def checkpoint(
    root: Path,
    session_id: str,
    project_id: str,
    memory: str,
) -> dict[str, object]:
    validate_memory(memory)
    path = anchor_path(root, session_id)
    with exclusive_file_lock(maintenance_lock(root)):
        require_tracked_project(root, project_id)
        with exclusive_file_lock(path.with_suffix(".lock")):
            anchor, _ = _ensure_anchor(
                root, session_id, project_id, allow_prepared=True
            )
            anchor["memory"] = memory.strip()
            anchor["archive_prepared_at"] = None
            anchor["last_activity_at"] = utc_now()
            write_anchor_file(path, anchor)
            return anchor


def restore(root: Path, session_id: str) -> dict[str, object]:
    path = anchor_path(root, session_id)
    with exclusive_file_lock(path.with_suffix(".lock")):
        anchor = read_anchor(root, session_id)
        if not anchor:
            raise ValueError("未找到会话锚点。")
        ensure_active(anchor)
        anchor["last_activity_at"] = utc_now()
        write_anchor_file(path, anchor)
        return anchor


def prepare_archive(root: Path, session_id: str) -> dict[str, object]:
    path = anchor_path(root, session_id)
    with exclusive_file_lock(path.with_suffix(".lock")):
        anchor = read_anchor(root, session_id)
        if not anchor:
            raise ValueError("未找到会话锚点。")
        ensure_active(anchor)
        if not str(anchor.get("memory", "")).strip():
            raise ValueError("归档前必须先保存会话摘要。")
        if not anchor.get("archive_prepared_at"):
            anchor["archive_prepared_at"] = utc_now()
            write_anchor_file(path, anchor)
        return anchor


def finalize_archive(root: Path, session_id: str) -> dict[str, object]:
    path = anchor_path(root, session_id)
    with exclusive_file_lock(path.with_suffix(".lock")):
        anchor = read_anchor(root, session_id)
        if not anchor:
            raise ValueError("未找到会话锚点。")
        if anchor.get("archived_at"):
            return anchor
        if not anchor.get("archive_prepared_at"):
            raise ValueError("会话尚未进入待归档状态。")
        return _archive_anchor(root, path, anchor)


def _archive_anchor(
    root: Path, path: Path, anchor: dict[str, object]
) -> dict[str, object]:
    memory = str(anchor.get("memory", "")).strip()
    memory_hash = memory_digest(memory)
    if memory and anchor.get("exported_memory_hash") != memory_hash:
        write_experience(root, anchor, memory)
        anchor["exported_memory_hash"] = memory_hash
    anchor["archived_at"] = utc_now()
    anchor["archive_prepared_at"] = None
    write_anchor_file(path, anchor)
    return anchor


def _anchor_state(anchor: dict[str, object]) -> str:
    if anchor.get("archived_at"):
        return "ARCHIVED"
    if anchor.get("archive_prepared_at"):
        return "ARCHIVE_PREPARED"
    return "ACTIVE"


def sweep(root: Path, days: int) -> list[str]:
    cutoff = datetime.now(timezone.utc) - timedelta(days=days)
    archived = []
    for path in (root / "sessions").glob("*/anchor.json"):
        with exclusive_file_lock(path.with_suffix(".lock")):
            try:
                anchor = json.loads(path.read_text(encoding="utf-8"))
                last_activity = datetime.fromisoformat(
                    str(anchor["last_activity_at"])
                )
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
            write_anchor_file(path, anchor)
            archived.append(str(anchor["session_key"]))
    return archived


def write_experience(
    root: Path,
    anchor: dict[str, object],
    memory: str,
) -> Path:
    validate_memory(memory)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    filename = f"{stamp}-session-{anchor['session_key']}.md"
    target = root / "personal-experience/candidates" / filename
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(
        "---\nstatus: CANDIDATE\nsource: session-anchor\n"
        f"session_key: {anchor['session_key']}\n"
        f"project_id: {anchor['project_id']}\n"
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
    parser = argparse.ArgumentParser(
        description="YTQJK session-anchor memory store."
    )
    parser.add_argument(
        "--knowledge-root",
        type=Path,
        default=default_knowledge_root(),
    )
    sub = parser.add_subparsers(dest="command", required=True)
    for name in (
        "anchor",
        "inspect",
        "checkpoint",
        "restore",
        "prepare-archive",
        "finalize-archive",
    ):
        item = sub.add_parser(name)
        item.add_argument("--session-id", required=True)
        if name in {"anchor", "inspect", "checkpoint"}:
            item.add_argument("--project-id", required=True)
        if name == "checkpoint":
            item.add_argument("--memory-file", type=Path, required=True)
    sweep_parser = sub.add_parser("sweep")
    sweep_parser.add_argument("--days", type=int, default=30)
    args = parser.parse_args()
    root = args.knowledge_root.resolve()
    if args.command == "anchor":
        result = write_anchor(root, args.session_id, args.project_id)
    elif args.command == "inspect":
        result = inspect_anchor(root, args.session_id, args.project_id)
    elif args.command == "checkpoint":
        memory = args.memory_file.read_text(encoding="utf-8")
        result = checkpoint(
            root,
            args.session_id,
            args.project_id,
            memory,
        )
    elif args.command == "restore":
        result = restore(root, args.session_id)
    elif args.command == "prepare-archive":
        result = prepare_archive(root, args.session_id)
    elif args.command == "finalize-archive":
        result = finalize_archive(root, args.session_id)
    else:
        result = {"archived": sweep(root, args.days)}
    print(json.dumps(result, ensure_ascii=False))


if __name__ == "__main__":
    main()
