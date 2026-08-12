from __future__ import annotations

import hashlib
import json
import shutil
import sqlite3
from contextlib import closing
from pathlib import Path
from typing import Any

from rag_common import atomic_json, load_json, utc_now


CACHE_NAME = "global-knowledge.sqlite3"
LEGACY_CACHE_NAME = "global-knowledge.json"
MAX_PROJECT_CACHE_BYTES = 1024**3


def query_prefetch(
    project_dir: Path, query: str, limit: int
) -> list[dict[str, object]]:
    database = project_dir / "cache" / CACHE_NAME
    if not database.is_file():
        _initialize(database, project_dir / "cache" / LEGACY_CACHE_NAME)
    enforce_project_capacity(project_dir)
    with closing(sqlite3.connect(database)) as connection:
        connection.row_factory = sqlite3.Row
        rows = _search(connection, query, limit)
        now = utc_now()
        connection.executemany(
            "UPDATE entries SET hit_count = hit_count + 1, last_accessed = ? WHERE id = ?",
            [(now, row["id"]) for row in rows],
        )
        connection.commit()
        return [_public_row(dict(row), hit_increment=1) for row in rows]


def update_prefetch(
    project_dir: Path,
    query: str,
    rows: list[dict[str, Any]],
    max_bytes: int = MAX_PROJECT_CACHE_BYTES,
) -> list[dict[str, object]]:
    database = project_dir / "cache" / CACHE_NAME
    _initialize(database, project_dir / "cache" / LEGACY_CACHE_NAME)
    now = utc_now()
    with closing(sqlite3.connect(database)) as connection:
        for row in rows:
            if not all(key in row for key in ("path", "line_start", "line_end", "content")):
                continue
            entry = _entry(row, query, now)
            connection.execute(
                "INSERT INTO entries VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "
                "ON CONFLICT(id) DO UPDATE SET content=excluded.content, query=excluded.query, "
                "cached_at=excluded.cached_at, last_accessed=excluded.last_accessed, "
                "hit_count=entries.hit_count + 1, size_bytes=excluded.size_bytes",
                entry,
            )
        connection.commit()
        _evict(connection, project_dir, max_bytes)
    enforce_project_capacity(project_dir, max_bytes)
    return list_prefetch(project_dir)


def list_prefetch(project_dir: Path, limit: int = 500) -> list[dict[str, object]]:
    database = project_dir / "cache" / CACHE_NAME
    legacy = project_dir / "cache" / LEGACY_CACHE_NAME
    if not database.is_file():
        if not legacy.is_file():
            return []
        _initialize(database, legacy)
    with closing(sqlite3.connect(database)) as connection:
        connection.row_factory = sqlite3.Row
        rows = connection.execute(
            "SELECT * FROM entries ORDER BY hit_count DESC, last_accessed DESC LIMIT ?",
            (max(1, min(limit, 500)),),
        ).fetchall()
        return [_public_row(dict(row)) for row in rows]


def prefetch_stats(project_dir: Path) -> dict[str, object]:
    entries = list_prefetch(project_dir)
    used = sum(int(entry["size_bytes"]) for entry in entries)
    total_used = _directory_size(project_dir)
    return {
        "entries": len(entries),
        "used_bytes": used,
        "project_used_bytes": total_used,
        "capacity_bytes": MAX_PROJECT_CACHE_BYTES,
        "capacity_exceeded": total_used > MAX_PROJECT_CACHE_BYTES,
        "policy": "LFU_LRU",
    }


def enforce_project_capacity(
    project_dir: Path, max_bytes: int = MAX_PROJECT_CACHE_BYTES
) -> list[str]:
    if max_bytes <= 0:
        raise ValueError("项目子库容量必须大于 0。")
    database = project_dir / "cache" / CACHE_NAME
    if database.is_file():
        with closing(sqlite3.connect(database)) as connection:
            _evict(connection, project_dir, max_bytes)
    evicted: list[str] = []
    vectors = project_dir / "vectors"
    if _directory_size(project_dir) > max_bytes and vectors.is_dir():
        shutil.rmtree(vectors)
        evicted.append("vectors")
    lexical = project_dir / "lexical.sqlite3"
    if _directory_size(project_dir) > max_bytes and lexical.is_file():
        lexical.unlink()
        evicted.append("lexical.sqlite3")
    if _directory_size(project_dir) > max_bytes:
        raise RuntimeError("项目知识子库清理可重建缓存后仍超过 1 GiB。")
    if evicted:
        manifest_path = project_dir / "manifest.json"
        manifest = load_json(manifest_path, {})
        if "vectors" in evicted:
            manifest["vector"] = {
                "enabled": False, "status": "EVICTED_CAPACITY", "error": None,
            }
        if "lexical.sqlite3" in evicted:
            manifest["indexed_at"] = None
            manifest["index_state"] = "EVICTED_CAPACITY"
        manifest["capacity_eviction"] = {
            "at": utc_now(), "evicted": evicted, "limit_bytes": max_bytes,
        }
        atomic_json(manifest_path, manifest)
    return evicted


def _initialize(database: Path, legacy: Path) -> None:
    database.parent.mkdir(parents=True, exist_ok=True)
    with closing(sqlite3.connect(database)) as connection:
        connection.execute(
            "CREATE TABLE IF NOT EXISTS entries (id TEXT PRIMARY KEY, path TEXT NOT NULL, "
            "line_start INTEGER NOT NULL, line_end INTEGER NOT NULL, content TEXT NOT NULL, "
            "query TEXT NOT NULL, cached_at TEXT NOT NULL, last_accessed TEXT NOT NULL, "
            "hit_count INTEGER NOT NULL, size_bytes INTEGER NOT NULL)"
        )
        connection.execute("CREATE INDEX IF NOT EXISTS entries_usage ON entries(hit_count, last_accessed)")
        connection.execute("CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)")
        migrated = connection.execute("SELECT value FROM metadata WHERE key = 'legacy_migrated'").fetchone()
        if migrated is None and legacy.is_file():
            try:
                entries = json.loads(legacy.read_text(encoding="utf-8")).get("entries", [])
            except (OSError, json.JSONDecodeError, AttributeError):
                entries = []
            now = utc_now()
            for row in entries if isinstance(entries, list) else []:
                if isinstance(row, dict) and all(key in row for key in ("path", "line_start", "line_end", "content")):
                    connection.execute("INSERT OR IGNORE INTO entries VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", _entry(row, str(row.get("query", "")), now))
        connection.execute("INSERT OR REPLACE INTO metadata VALUES ('legacy_migrated', '1')")
        connection.commit()


def _entry(row: dict[str, Any], query: str, now: str) -> tuple[object, ...]:
    path = str(row["path"])
    line_start, line_end = int(row["line_start"]), int(row["line_end"])
    content = str(row["content"])
    identifier = hashlib.sha256(f"{path}:{line_start}:{line_end}".encode("utf-8")).hexdigest()
    size = len(path.encode("utf-8")) + len(content.encode("utf-8"))
    return identifier, path, line_start, line_end, content, query, now, now, 1, size


def _search(connection: sqlite3.Connection, query: str, limit: int) -> list[sqlite3.Row]:
    normalized = query.strip()
    if not normalized:
        return []
    terms = list(dict.fromkeys([normalized, *normalized.split()]))[:8]
    where = " OR ".join("content LIKE ?" for _ in terms)
    return connection.execute(
        f"SELECT * FROM entries WHERE {where} ORDER BY hit_count DESC, last_accessed DESC LIMIT ?",
        (*[f"%{term}%" for term in terms], max(1, min(limit, 20))),
    ).fetchall()


def _evict(connection: sqlite3.Connection, project_dir: Path, max_bytes: int) -> None:
    if max_bytes <= 0:
        raise ValueError("项目子库容量必须大于 0。")
    while _directory_size(project_dir) > max_bytes:
        excess = _directory_size(project_dir) - max_bytes
        victims = connection.execute(
            "SELECT id, size_bytes FROM entries ORDER BY hit_count ASC, last_accessed ASC"
        ).fetchall()
        if not victims:
            return
        freed = 0
        for identifier, size_bytes in victims:
            connection.execute("DELETE FROM entries WHERE id = ?", (identifier,))
            freed += int(size_bytes)
            if freed >= excess:
                break
        connection.commit()
        connection.execute("VACUUM")


def _directory_size(directory: Path) -> int:
    return sum(
        path.stat().st_size
        for path in directory.rglob("*")
        if path.is_file()
        and path.relative_to(directory).parts[0] not in {"handoffs", "errors"}
    )


def _all_rows(connection: sqlite3.Connection) -> list[sqlite3.Row]:
    connection.row_factory = sqlite3.Row
    return connection.execute(
        "SELECT * FROM entries ORDER BY hit_count DESC, last_accessed DESC"
    ).fetchall()


def _public_row(row: dict[str, object], hit_increment: int = 0) -> dict[str, object]:
    row.pop("id", None)
    if hit_increment:
        row["hit_count"] = int(row["hit_count"]) + hit_increment
    row["scope"] = "project-prefetch-cache"
    return row
