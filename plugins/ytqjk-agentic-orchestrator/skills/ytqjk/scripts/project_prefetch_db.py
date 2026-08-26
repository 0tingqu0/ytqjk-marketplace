"""SQLite schema and row helpers for project prefetch caches."""

from __future__ import annotations

import hashlib
import json
import sqlite3
from contextlib import closing
from pathlib import Path
from typing import Any

from global_store import is_approved_path, is_current_approved_hit
from rag_common import utc_now


APPROVED_SQL_GLOBS = (
    "verified/*",
    "personal-experience/approved/*",
    "error-experience/approved/*",
)
APPROVED_SQL_WHERE = " OR ".join(
    "REPLACE(path, '\\', '/') GLOB ?"
    for _ in APPROVED_SQL_GLOBS
)


def initialize(database: Path, legacy: Path) -> None:
    database.parent.mkdir(parents=True, exist_ok=True)
    with closing(sqlite3.connect(database)) as connection:
        connection.execute(
            "CREATE TABLE IF NOT EXISTS entries ("
            "id TEXT PRIMARY KEY, path TEXT NOT NULL, "
            "line_start INTEGER NOT NULL, line_end INTEGER NOT NULL, "
            "content TEXT NOT NULL, source_sha256 TEXT NOT NULL, "
            "query TEXT NOT NULL, cached_at TEXT NOT NULL, "
            "last_accessed TEXT NOT NULL, hit_count INTEGER NOT NULL, "
            "size_bytes INTEGER NOT NULL)"
        )
        columns = {
            row[1]
            for row in connection.execute("PRAGMA table_info(entries)")
        }
        if "source_sha256" not in columns:
            connection.execute(
                "ALTER TABLE entries ADD COLUMN "
                "source_sha256 TEXT NOT NULL DEFAULT ''"
            )
        connection.execute(
            "CREATE INDEX IF NOT EXISTS entries_usage "
            "ON entries(hit_count, last_accessed)"
        )
        connection.execute(
            "CREATE TABLE IF NOT EXISTS metadata ("
            "key TEXT PRIMARY KEY, value TEXT NOT NULL)"
        )
        _migrate_legacy(connection, legacy)
        connection.execute(
            "INSERT OR REPLACE INTO metadata VALUES "
            "('legacy_migrated', '1')"
        )
        connection.commit()


def _migrate_legacy(
    connection: sqlite3.Connection,
    legacy: Path,
) -> None:
    migrated = connection.execute(
        "SELECT value FROM metadata WHERE key = 'legacy_migrated'"
    ).fetchone()
    if migrated is not None or not legacy.is_file():
        return
    try:
        content = legacy.read_text(encoding="utf-8")
        entries = json.loads(content).get("entries", [])
    except (OSError, json.JSONDecodeError, AttributeError):
        entries = []
    now = utc_now()
    for row in entries if isinstance(entries, list) else []:
        if not _legacy_row(row):
            continue
        connection.execute(
            "INSERT OR IGNORE INTO entries ("
            "id, path, line_start, line_end, content, "
            "source_sha256, query, cached_at, last_accessed, "
            "hit_count, size_bytes) VALUES "
            "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
            make_entry(row, str(row.get("query", "")), now),
        )


def _legacy_row(row: object) -> bool:
    return (
        isinstance(row, dict)
        and all(
            key in row for key in (
                "path", "line_start", "line_end",
                "content", "source_sha256",
            )
        )
        and is_approved_path(str(row["path"]))
    )


def ensure_generation(
    connection: sqlite3.Connection,
    generation: str | None,
) -> None:
    if generation is None:
        return
    current = connection.execute(
        "SELECT value FROM metadata "
        "WHERE key = 'global_generation'"
    ).fetchone()
    if current is None or current[0] != generation:
        connection.execute("DELETE FROM entries")
        connection.execute(
            "INSERT OR REPLACE INTO metadata VALUES "
            "('global_generation', ?)",
            (generation,),
        )
        connection.commit()


def current_generation(
    connection: sqlite3.Connection,
) -> str | None:
    row = connection.execute(
        "SELECT value FROM metadata "
        "WHERE key = 'global_generation'"
    ).fetchone()
    return str(row[0]) if row else None


def make_entry(
    row: dict[str, Any],
    query: str,
    now: str,
) -> tuple[object, ...]:
    path = str(row["path"])
    line_start = int(row["line_start"])
    line_end = int(row["line_end"])
    content = str(row["content"])
    source_hash = str(row["source_sha256"])
    raw_id = f"{path}:{line_start}:{line_end}"
    identifier = hashlib.sha256(raw_id.encode("utf-8")).hexdigest()
    size = sum(
        len(value.encode("utf-8"))
        for value in (path, content, source_hash)
    )
    return (
        identifier, path, line_start, line_end, content,
        source_hash, query, now, now, 1, size,
    )


def purge_unapproved(connection: sqlite3.Connection) -> None:
    paths = connection.execute("SELECT id, path FROM entries").fetchall()
    invalid = [
        (identifier,)
        for identifier, path in paths
        if not is_approved_path(path)
    ]
    if invalid:
        connection.executemany(
            "DELETE FROM entries WHERE id = ?", invalid
        )
        connection.commit()


def retain_current_sources(
    connection: sqlite3.Connection,
    rows: list[sqlite3.Row],
    knowledge_root: Path,
) -> list[sqlite3.Row]:
    current: list[sqlite3.Row] = []
    invalid: list[tuple[object]] = []
    for row in rows:
        if not is_current_approved_hit(knowledge_root, dict(row)):
            invalid.append((row["id"],))
        else:
            current.append(row)
    if invalid:
        connection.executemany(
            "DELETE FROM entries WHERE id = ?", invalid
        )
        connection.commit()
    return current


def search(
    connection: sqlite3.Connection,
    query: str,
    limit: int,
) -> list[sqlite3.Row]:
    normalized = query.strip()
    if not normalized:
        return []
    terms = list(
        dict.fromkeys([normalized, *normalized.split()])
    )[:8]
    content_where = " OR ".join(
        "content LIKE ?" for _ in terms
    )
    return connection.execute(
        "SELECT * FROM entries WHERE "
        f"({APPROVED_SQL_WHERE}) AND ({content_where}) "
        "ORDER BY hit_count DESC, last_accessed DESC LIMIT ?",
        (
            *APPROVED_SQL_GLOBS,
            *[f"%{term}%" for term in terms],
            max(1, min(limit, 20)),
        ),
    ).fetchall()


__all__ = [
    "APPROVED_SQL_GLOBS",
    "APPROVED_SQL_WHERE",
    "current_generation",
    "ensure_generation",
    "initialize",
    "make_entry",
    "purge_unapproved",
    "retain_current_sources",
    "search",
]
