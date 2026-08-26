"""Capacity policy for rebuildable project knowledge caches."""

from __future__ import annotations

import shutil
import sqlite3
from contextlib import closing
from pathlib import Path

from rag_common import atomic_json, load_json, utc_now


CACHE_NAME = "global-knowledge.sqlite3"
LEGACY_CACHE_NAME = "global-knowledge.json"
MAX_PROJECT_CACHE_BYTES = 1024**3


def directory_size(directory: Path) -> int:
    return sum(
        path.stat().st_size
        for path in directory.rglob("*")
        if path.is_file()
        and path.relative_to(directory).parts[0]
        not in {"handoffs", "errors"}
    )


def known_project_size(project_dir: Path) -> int:
    paths = (
        project_dir / "manifest.json",
        project_dir / "lexical.sqlite3",
        project_dir / "cache" / CACHE_NAME,
        project_dir / "cache" / LEGACY_CACHE_NAME,
    )
    return sum(
        path.stat().st_size for path in paths if path.is_file()
    )


def evict_entries(
    connection: sqlite3.Connection,
    project_dir: Path,
    max_bytes: int,
) -> None:
    if max_bytes <= 0:
        raise ValueError("项目子库容量必须大于 0。")
    while directory_size(project_dir) > max_bytes:
        excess = directory_size(project_dir) - max_bytes
        victims = connection.execute(
            "SELECT id, size_bytes FROM entries "
            "ORDER BY hit_count ASC, last_accessed ASC"
        ).fetchall()
        if not victims:
            return
        freed = 0
        for identifier, size_bytes in victims:
            connection.execute(
                "DELETE FROM entries WHERE id = ?",
                (identifier,),
            )
            freed += int(size_bytes)
            if freed >= excess:
                break
        connection.commit()
        connection.execute("VACUUM")


def enforce_project_capacity(
    project_dir: Path,
    max_bytes: int = MAX_PROJECT_CACHE_BYTES,
) -> list[str]:
    if max_bytes <= 0:
        raise ValueError("项目子库容量必须大于 0。")
    database = project_dir / "cache" / CACHE_NAME
    if database.is_file():
        with closing(sqlite3.connect(database)) as connection:
            evict_entries(connection, project_dir, max_bytes)
    evicted: list[str] = []
    vectors = project_dir / "vectors"
    if directory_size(project_dir) > max_bytes and vectors.is_dir():
        shutil.rmtree(vectors)
        evicted.append("vectors")
    lexical = project_dir / "lexical.sqlite3"
    if directory_size(project_dir) > max_bytes and lexical.is_file():
        lexical.unlink()
        evicted.append("lexical.sqlite3")
    final_size = directory_size(project_dir)
    if final_size > max_bytes:
        raise RuntimeError(
            "项目知识子库清理可重建缓存后仍超过 1 GiB。"
        )
    _record_capacity(project_dir, max_bytes, final_size, evicted)
    return evicted


def _record_capacity(
    project_dir: Path,
    max_bytes: int,
    final_size: int,
    evicted: list[str],
) -> None:
    manifest_path = project_dir / "manifest.json"
    manifest = load_json(manifest_path, {})
    manifest["capacity"] = {
        "used_bytes": final_size,
        "limit_bytes": max_bytes,
        "checked_at": utc_now(),
    }
    if "vectors" in evicted:
        manifest["vector"] = {
            "enabled": False,
            "status": "EVICTED_CAPACITY",
            "error": None,
        }
    if "lexical.sqlite3" in evicted:
        manifest["indexed_at"] = None
        manifest["index_state"] = "EVICTED_CAPACITY"
    if evicted:
        manifest["capacity_eviction"] = {
            "at": utc_now(),
            "evicted": evicted,
            "limit_bytes": max_bytes,
        }
    atomic_json(manifest_path, manifest)


__all__ = [
    "CACHE_NAME",
    "LEGACY_CACHE_NAME",
    "MAX_PROJECT_CACHE_BYTES",
    "directory_size",
    "enforce_project_capacity",
    "evict_entries",
    "known_project_size",
]
