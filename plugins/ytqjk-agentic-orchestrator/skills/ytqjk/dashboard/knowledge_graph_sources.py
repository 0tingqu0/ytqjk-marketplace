"""Bounded, read-only corpus loading for the dashboard knowledge graph."""

from __future__ import annotations

import json
import sqlite3
from dataclasses import dataclass
from pathlib import Path

from global_store import APPROVED_ROOTS, load_approved_source


MAX_SOURCE_CHARS = 8_000
MAX_INDEX_CHUNKS = 1_200


@dataclass(frozen=True)
class GraphSource:
    scope: str
    project_id: str | None
    path: str
    line_start: int
    line_end: int
    content: str
    indexed_at: str | None = None

    @property
    def document_key(self) -> str:
        return f"{self.scope}\0{self.path}"


def _approved_sources(root: Path) -> list[GraphSource]:
    rows: list[GraphSource] = []
    for relative_root in APPROVED_ROOTS:
        directory = root / relative_root
        if not directory.is_dir():
            continue
        for path in sorted(directory.rglob("*.md")):
            relative = path.relative_to(root).as_posix()
            approved = load_approved_source(root, relative)
            if approved is None:
                continue
            lines = approved.text.splitlines()
            for start in range(0, len(lines), 120):
                content = "\n".join(lines[start:start + 120]).strip()
                if content:
                    rows.append(GraphSource(
                        "global", None, relative, start + 1,
                        min(start + 120, len(lines)),
                        content[:MAX_SOURCE_CHARS],
                    ))
    return rows


def _read_manifest(path: Path) -> dict[str, object]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
        return value if isinstance(value, dict) else {}
    except (OSError, json.JSONDecodeError):
        return {}


def _index_sources(project_dir: Path, limit: int) -> list[GraphSource]:
    database = project_dir / "lexical.sqlite3"
    if not database.is_file():
        return []
    manifest = _read_manifest(project_dir / "manifest.json")
    identity = manifest.get("identity", {})
    project_id = (
        str(identity.get("id", project_dir.name))
        if isinstance(identity, dict)
        else project_dir.name
    )
    indexed_at = manifest.get("indexed_at")
    connection = sqlite3.connect(database)
    try:
        columns = {
            row[1] for row in connection.execute("PRAGMA table_info(chunks)")
        }
        required = {"path", "line_start", "line_end", "content"}
        if not required <= columns:
            return []
        rows = connection.execute(
            "SELECT path, line_start, line_end, content FROM chunks "
            "ORDER BY path, line_start LIMIT ?",
            (limit,),
        ).fetchall()
    except sqlite3.Error:
        return []
    finally:
        connection.close()
    return [
        GraphSource(
            f"project:{project_id}", project_id, str(row[0]),
            int(row[1]), int(row[2]), str(row[3])[:MAX_SOURCE_CHARS],
            str(indexed_at) if indexed_at else None,
        )
        for row in rows
    ]


def load_graph_sources(
    root: Path, max_index_chunks: int = MAX_INDEX_CHUNKS,
) -> list[GraphSource]:
    """Return current approved documents plus bounded project index chunks."""
    root = root.resolve()
    sources = _approved_sources(root)
    projects = root / "projects"
    directories = (
        sorted(path for path in projects.iterdir() if path.is_dir())
        if projects.is_dir()
        else []
    )
    remaining = max(0, max_index_chunks)
    for index, project_dir in enumerate(directories):
        slots = max(1, len(directories) - index)
        allowance = remaining // slots if remaining else 0
        indexed = _index_sources(project_dir, allowance)
        sources.extend(indexed)
        remaining -= len(indexed)
    seen: set[tuple[object, ...]] = set()
    unique: list[GraphSource] = []
    for source in sources:
        key = (
            source.scope, source.path, source.line_start,
            source.line_end, source.content,
        )
        if key not in seen:
            seen.add(key)
            unique.append(source)
    return unique


def vector_index_available(root: Path) -> bool:
    candidates = [root / "global-cache"]
    projects = root / "projects"
    if projects.is_dir():
        candidates.extend(path for path in projects.iterdir() if path.is_dir())
    for directory in candidates:
        vector = _read_manifest(directory / "manifest.json").get("vector", {})
        if isinstance(vector, dict) and vector.get("enabled") is True:
            return True
    return False


__all__ = [
    "GraphSource", "load_graph_sources", "vector_index_available",
]
