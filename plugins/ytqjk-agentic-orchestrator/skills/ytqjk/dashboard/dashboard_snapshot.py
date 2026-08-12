from __future__ import annotations

import json
from pathlib import Path

from approval_promotion import promote_eligible
from archive_sync import sync_archived_sessions


SECTIONS = (
    ("verified", "已验证", "verified"),
    ("personal-experience/approved", "个人经验", "approved"),
    ("error-experience/approved", "错误经验", "approved"),
    ("personal-experience/candidates", "个人候选", "candidate"),
    ("error-experience/candidates", "错误候选", "candidate"),
)


def read_json(path: Path) -> dict[str, object]:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def snapshot(root: Path, safe_document: object) -> dict[str, object]:
    sync_archived_sessions(root)
    promote_eligible(root)
    documents = [
        row for relative, label, state in SECTIONS for row in relative_files(root, relative, label, state, safe_document)
    ]
    sessions = session_rows(root)
    return {
        "root": str(root), "config": read_json(root / "config.json"),
        "global": read_json(root / "global-cache" / "manifest.json"),
        "projects": project_rows(root), "sessions": sessions, "documents": documents,
        "counts": {
            "verified": sum(item["state"] == "verified" for item in documents),
            "approved": sum(item["state"] == "approved" for item in documents),
            "candidate": sum(item["state"] == "candidate" for item in documents),
            "sessions": len(sessions),
        },
    }


def relative_files(root: Path, relative: str, label: str, state: str, safe_document: object) -> list[dict[str, object]]:
    directory = root / relative
    if not directory.is_dir():
        return []
    rows = []
    for path in sorted(directory.rglob("*.md")):
        display_path = path.relative_to(root).as_posix()
        if not callable(safe_document) or safe_document(root, display_path) is None:
            continue
        rows.append({"path": display_path, "label": label, "state": state, "bytes": path.stat().st_size, "modified": path.stat().st_mtime})
    return rows


def project_rows(root: Path) -> list[dict[str, object]]:
    rows = []
    for manifest_path in sorted((root / "projects").glob("*/manifest.json")):
        manifest, identity, stats = read_json(manifest_path), read_json(manifest_path).get("identity", {}), read_json(manifest_path).get("stats", {})
        vector = manifest.get("vector", {})
        if not isinstance(identity, dict) or not isinstance(stats, dict):
            continue
        rows.append({"id": identity.get("id", manifest_path.parent.name), "name": identity.get("name", manifest_path.parent.name), "head": identity.get("head", "UNKNOWN"), "dirty": identity.get("dirty", "unknown"), "indexed_at": manifest.get("indexed_at"), "files": stats.get("files", 0), "chunks": stats.get("chunks", 0), "text_bytes": stats.get("text_bytes", 0), "vector": vector.get("status", "NOT_BUILT") if isinstance(vector, dict) else "UNKNOWN"})
    return rows


def session_rows(root: Path) -> list[dict[str, object]]:
    rows = []
    for path in sorted((root / "sessions").glob("*/anchor.json")):
        anchor = read_json(path)
        key, project = anchor.get("session_key"), anchor.get("project_id")
        if isinstance(key, str) and isinstance(project, str):
            rows.append({"key": key[:12], "project": project, "created_at": anchor.get("created_at"), "last_activity_at": anchor.get("last_activity_at"), "archived_at": anchor.get("archived_at"), "has_memory": bool(anchor.get("memory"))})
    return sorted(rows, key=lambda item: str(item["last_activity_at"] or ""), reverse=True)
