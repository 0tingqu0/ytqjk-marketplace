from __future__ import annotations

import sqlite3
from contextlib import closing
from pathlib import Path
from typing import Any

from global_store import capture_approved_hit
from project_cache_capacity import (
    CACHE_NAME,
    LEGACY_CACHE_NAME,
    MAX_PROJECT_CACHE_BYTES,
    directory_size as _directory_size,
    enforce_project_capacity,
    evict_entries,
    known_project_size,
)
from project_prefetch_db import (
    APPROVED_SQL_GLOBS,
    APPROVED_SQL_WHERE,
    current_generation,
    ensure_generation,
    initialize,
    make_entry,
    purge_unapproved,
    retain_current_sources,
    search,
)
from rag_common import load_json, utc_now


_purge_unapproved = purge_unapproved


def query_prefetch(
    project_dir: Path,
    query: str,
    limit: int,
    generation: str | None = None,
    require_generation: bool = False,
    knowledge_root: Path | None = None,
) -> list[dict[str, object]]:
    root = _knowledge_root(project_dir, knowledge_root)
    if root is None:
        return []
    database = project_dir / "cache" / CACHE_NAME
    if not database.is_file():
        initialize(database, project_dir / "cache" / LEGACY_CACHE_NAME)
    with closing(sqlite3.connect(database)) as connection:
        ensure_generation(connection, generation)
        if require_generation and not current_generation(connection):
            connection.execute("DELETE FROM entries")
            connection.commit()
            return []
        connection.row_factory = sqlite3.Row
        rows = search(connection, query, limit)
        rows = retain_current_sources(connection, rows, root)
        now = utc_now()
        connection.executemany(
            "UPDATE entries SET hit_count = hit_count + 1, "
            "last_accessed = ? WHERE id = ?",
            [(now, row["id"]) for row in rows],
        )
        connection.commit()
        return [_public_row(dict(row), hit_increment=1) for row in rows]


def update_prefetch(
    project_dir: Path,
    query: str,
    rows: list[dict[str, Any]],
    max_bytes: int = MAX_PROJECT_CACHE_BYTES,
    generation: str | None = None,
    allow_generation_change: bool = True,
    knowledge_root: Path | None = None,
) -> list[dict[str, object]]:
    root = _knowledge_root(project_dir, knowledge_root)
    if root is None:
        return []
    database = project_dir / "cache" / CACHE_NAME
    initialize(database, project_dir / "cache" / LEGACY_CACHE_NAME)
    now = utc_now()
    with closing(sqlite3.connect(database)) as connection:
        if generation and not allow_generation_change:
            current = current_generation(connection)
            if current and current != generation:
                return []
        ensure_generation(connection, generation)
        purge_unapproved(connection)
        for row in rows:
            captured = capture_approved_hit(root, row)
            if captured is None:
                continue
            entry = make_entry(captured, query, now)
            connection.execute(
                "INSERT INTO entries ("
                "id, path, line_start, line_end, content, "
                "source_sha256, query, cached_at, last_accessed, "
                "hit_count, size_bytes) VALUES "
                "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "
                "ON CONFLICT(id) DO UPDATE SET "
                "content=excluded.content, "
                "source_sha256=excluded.source_sha256, "
                "query=excluded.query, "
                "cached_at=excluded.cached_at, "
                "last_accessed=excluded.last_accessed, "
                "hit_count=entries.hit_count + 1, "
                "size_bytes=excluded.size_bytes",
                entry,
            )
        connection.commit()
        connection.row_factory = sqlite3.Row
        current = connection.execute("SELECT * FROM entries").fetchall()
        retain_current_sources(connection, current, root)
        evict_entries(connection, project_dir, max_bytes)
    enforce_project_capacity(project_dir, max_bytes)
    return list_prefetch(project_dir, knowledge_root=root)


def sync_prefetch_generation(project_dir: Path, generation: str) -> None:
    database = project_dir / "cache" / CACHE_NAME
    initialize(database, project_dir / "cache" / LEGACY_CACHE_NAME)
    with closing(sqlite3.connect(database)) as connection:
        ensure_generation(connection, generation)
        purge_unapproved(connection)


def list_prefetch(
    project_dir: Path,
    limit: int = 500,
    knowledge_root: Path | None = None,
) -> list[dict[str, object]]:
    database = project_dir / "cache" / CACHE_NAME
    legacy = project_dir / "cache" / LEGACY_CACHE_NAME
    if not database.is_file():
        if not legacy.is_file():
            return []
        initialize(database, legacy)
    with closing(sqlite3.connect(database)) as connection:
        purge_unapproved(connection)
        connection.row_factory = sqlite3.Row
        rows = connection.execute(
            "SELECT * FROM entries ORDER BY hit_count DESC, "
            "last_accessed DESC LIMIT ?",
            (max(1, min(limit, 500)),),
        ).fetchall()
        root = _knowledge_root(project_dir, knowledge_root)
        if root is not None:
            rows = retain_current_sources(connection, rows, root)
        return [_public_row(dict(row)) for row in rows]


def prefetch_stats(project_dir: Path) -> dict[str, object]:
    database = project_dir / "cache" / CACHE_NAME
    legacy = project_dir / "cache" / LEGACY_CACHE_NAME
    if not database.is_file() and legacy.is_file():
        initialize(database, legacy)
    entries = 0
    used = 0
    total_used = known_project_size(project_dir)
    manifest = load_json(project_dir / "manifest.json", {})
    capacity = manifest.get("capacity", {})
    if isinstance(capacity, dict) and "used_bytes" in capacity:
        total_used = max(total_used, int(capacity["used_bytes"]))
    if database.is_file():
        with closing(sqlite3.connect(database)) as connection:
            entries, used = connection.execute(
                "SELECT COUNT(*), COALESCE(SUM(size_bytes), 0) FROM entries "
                f"WHERE {APPROVED_SQL_WHERE}",
                APPROVED_SQL_GLOBS,
            ).fetchone()
    return {
        "entries": int(entries),
        "used_bytes": int(used),
        "project_used_bytes": total_used,
        "capacity_bytes": MAX_PROJECT_CACHE_BYTES,
        "capacity_exceeded": total_used > MAX_PROJECT_CACHE_BYTES,
        "policy": "LFU_LRU",
    }

def _knowledge_root(
    project_dir: Path,
    supplied: Path | None,
) -> Path | None:
    if supplied is not None:
        return supplied
    if project_dir.parent.name != "projects":
        return None
    return project_dir.parent.parent


def _public_row(
    row: dict[str, object],
    hit_increment: int = 0,
) -> dict[str, object]:
    row.pop("id", None)
    if hit_increment:
        row["hit_count"] = int(row["hit_count"]) + hit_increment
    row["scope"] = "project-prefetch-cache"
    return row
