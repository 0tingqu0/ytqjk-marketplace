from __future__ import annotations

import json
import sqlite3
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
        "global_library": global_library(root, documents),
        "projects": project_rows(root), "sessions": sessions, "documents": documents,
        "counts": {
            "verified": sum(item["state"] == "verified" for item in documents),
            "approved": sum(item["state"] == "approved" for item in documents),
            "candidate": sum(item["state"] == "candidate" for item in documents),
            "sessions": len(sessions),
        },
    }


def global_library(root: Path, documents: list[dict[str, object]]) -> dict[str, object]:
    manifest = read_json(root / "global-cache" / "manifest.json")
    stats = manifest.get("stats", {})
    return {
        "path": str(root), "indexed_at": manifest.get("indexed_at"),
        "files": stats.get("files", 0) if isinstance(stats, dict) else 0,
        "chunks": stats.get("chunks", 0) if isinstance(stats, dict) else 0,
        "verified": sum(item["state"] == "verified" for item in documents),
        "approved": sum(item["state"] == "approved" for item in documents),
        "candidate": sum(item["state"] == "candidate" for item in documents),
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
    catalog = read_json(root / "catalog.json").get("projects", {})
    project_root = root / "projects"
    directories = {path.name for path in project_root.iterdir() if path.is_dir()} if project_root.is_dir() else set()
    catalog_ids = set(catalog) if isinstance(catalog, dict) else set()
    for project_id in sorted(directories | catalog_ids):
        manifest = read_json(project_root / project_id / "manifest.json")
        identity, stats = manifest.get("identity", {}), manifest.get("stats", {})
        vector = manifest.get("vector", {})
        catalog_row = catalog.get(project_id, {}) if isinstance(catalog, dict) else {}
        if not isinstance(identity, dict): identity = {}
        if not isinstance(stats, dict): stats = {}
        if not isinstance(catalog_row, dict): catalog_row = {}
        rows.append({"id": identity.get("id", project_id), "name": identity.get("name", catalog_row.get("name", project_id)), "head": identity.get("head", "未索引"), "dirty": identity.get("dirty", "unknown"), "indexed_at": manifest.get("indexed_at"), "files": stats.get("files", 0), "chunks": stats.get("chunks", 0), "text_bytes": stats.get("text_bytes", 0), "vector": vector.get("status", "NOT_BUILT") if isinstance(vector, dict) else "NOT_BUILT", "tracking": catalog_row.get("tracking_state", "INDEXED" if manifest else "REGISTERED")})
    return rows


def project_library(root: Path, project_id: str) -> dict[str, object] | None:
    if not project_id or any(char not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-" for char in project_id):
        return None
    project_dir = root / "projects" / project_id
    manifest = read_json(project_dir / "manifest.json")
    identity, stats = manifest.get("identity", {}), manifest.get("stats", {})
    if not isinstance(identity, dict) or not isinstance(stats, dict):
        return None
    database = project_dir / "lexical.sqlite3"
    chunks = read_project_chunks(database) if database.is_file() else []
    files: dict[str, list[dict[str, object]]] = {}
    for chunk in chunks:
        files.setdefault(str(chunk["path"]), []).append(chunk)
    return {
        "id": project_id, "name": identity.get("name", project_id),
        "indexed_at": manifest.get("indexed_at"), "files": list(files.values()),
        "file_count": len(files), "chunk_count": len(chunks),
        "expected_files": stats.get("files", 0), "expected_chunks": stats.get("chunks", 0),
    }


def read_project_chunks(database: Path) -> list[dict[str, object]]:
    connection = sqlite3.connect(database)
    try:
        rows = connection.execute(
            "SELECT path, line_start, line_end, content FROM chunks ORDER BY path, line_start LIMIT 500"
        ).fetchall()
    except sqlite3.Error:
        return []
    finally:
        connection.close()
    return [
        {"path": row[0], "line_start": row[1], "line_end": row[2], "content": row[3]}
        for row in rows
    ]


def session_rows(root: Path) -> list[dict[str, object]]:
    rows = []
    for path in sorted((root / "sessions").glob("*/anchor.json")):
        anchor = read_json(path)
        key, project = anchor.get("session_key"), anchor.get("project_id")
        if isinstance(key, str) and isinstance(project, str):
            rows.append({"key": key[:12], "project": project, "created_at": anchor.get("created_at"), "last_activity_at": anchor.get("last_activity_at"), "archived_at": anchor.get("archived_at"), "has_memory": bool(anchor.get("memory"))})
    return sorted(rows, key=lambda item: str(item["last_activity_at"] or ""), reverse=True)
