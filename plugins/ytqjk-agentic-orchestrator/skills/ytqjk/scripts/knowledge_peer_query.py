"""Read-only subtree query surface for authenticated LAN peers."""

from __future__ import annotations

import hashlib
import sqlite3
from contextlib import closing
from pathlib import Path
from typing import Any

from global_store import is_current_approved_hit
from knowledge_peer_scope import (
    PeerLibrary,
    PeerScopeError,
    exported_libraries,
    require_exported_library,
)
from project_prefetch_db import search
from rag_common import SCHEMA_VERSION, lexical_query, load_json


MAX_RESULTS = 20
MAX_CONTENT_CHARS = 24_000


class PeerQueryError(RuntimeError):
    """Peer subtree query cannot be served safely."""


def query_library_subtree(
    root: Path,
    project_id: str,
    export_node_id: str,
    query: str,
    limit: int,
) -> dict[str, object]:
    _request(project_id, query, limit)
    libraries = _libraries(root, project_id, export_node_id)
    results: list[dict[str, object]] = []
    generations: list[str] = []
    for library in libraries:
        if len(results) >= limit:
            break
        rows, generation = _query_library(
            root, library, query, limit - len(results)
        )
        results.extend(rows)
        if generation:
            generations.append(f"{library.node_id}:{generation}")
    return {
        "status": "PEER_HIT" if results else "PEER_MISS",
        "project_id": project_id,
        "node_id": export_node_id,
        "generation": "|".join(generations),
        "results": results[:limit],
    }


def query_project_library(
    root: Path,
    project_id: str,
    query: str,
    limit: int,
) -> dict[str, object]:
    return query_library_subtree(
        root, project_id, project_id, query, limit
    )


def fetch_subtree_material(
    root: Path,
    project_id: str,
    export_node_id: str,
    library_node: str,
    material_id: str,
) -> dict[str, object]:
    try:
        library = require_exported_library(
            root, project_id, export_node_id, library_node
        )
    except PeerScopeError as error:
        raise PeerQueryError(str(error)) from error
    source, identifier = _material_id(material_id)
    if library.kind == "project":
        if source not in {"project", "prefetch"}:
            raise PeerQueryError("INVALID_MATERIAL_ID")
        database = (
            library.directory / "lexical.sqlite3"
            if source == "project"
            else library.directory / "cache" / "global-knowledge.sqlite3"
        )
        table = "chunks" if source == "project" else "entries"
    else:
        if source != "library":
            raise PeerQueryError("INVALID_MATERIAL_ID")
        database = library.directory / "lexical.sqlite3"
        table = "chunks"
    row = _row_by_id(database, table, identifier)
    if row is None:
        raise PeerQueryError("PEER_MATERIAL_NOT_FOUND")
    if source != "project" and not is_current_approved_hit(root, row):
        raise PeerQueryError("PEER_MATERIAL_REVOKED")
    return _public(
        row, source, _scope(library, source), library.node_id
    )


def fetch_project_material(
    root: Path,
    project_id: str,
    material_id: str,
) -> dict[str, object]:
    return fetch_subtree_material(
        root, project_id, project_id, project_id, material_id
    )


def _libraries(
    root: Path,
    project_id: str,
    export_node_id: str,
) -> tuple[PeerLibrary, ...]:
    try:
        return exported_libraries(root, project_id, export_node_id)
    except PeerScopeError as error:
        raise PeerQueryError(str(error)) from error


def _query_library(
    root: Path,
    library: PeerLibrary,
    query: str,
    limit: int,
) -> tuple[list[dict[str, object]], str]:
    manifest = load_json(library.directory / "manifest.json", {})
    database = library.directory / "lexical.sqlite3"
    results: list[dict[str, object]] = []
    if database.is_file():
        if manifest.get("schema_version") != SCHEMA_VERSION:
            raise PeerQueryError("PEER_LIBRARY_INDEX_INVALID")
        rows = lexical_query(database, query, limit)
        if library.kind != "project":
            rows = [
                row for row in rows
                if is_current_approved_hit(root, row)
            ]
        source = "project" if library.kind == "project" else "library"
        results.extend(
            _public(row, source, _scope(library, source), library.node_id)
            for row in rows
        )
    if library.kind == "project" and len(results) < limit:
        results.extend(
            _prefetch_rows(
                root,
                library,
                query,
                limit - len(results),
            )
        )
    generation = str(
        manifest.get("generation")
        or manifest.get("source_fingerprint")
        or manifest.get("indexed_at")
        or ""
    )
    return results, generation


def _prefetch_rows(
    root: Path,
    library: PeerLibrary,
    query: str,
    limit: int,
) -> list[dict[str, object]]:
    database = library.directory / "cache" / "global-knowledge.sqlite3"
    if not database.is_file() or limit < 1:
        return []
    with _read_only(database) as connection:
        connection.row_factory = sqlite3.Row
        rows = search(connection, query, limit)
        current = [
            dict(row) for row in rows
            if is_current_approved_hit(root, dict(row))
        ]
    return [
        _public(
            row,
            "prefetch",
            "peer-approved-cache",
            library.node_id,
        )
        for row in current
    ]


def _row_by_id(
    database: Path,
    table: str,
    identifier: str,
) -> dict[str, Any] | None:
    if not database.is_file():
        return None
    with _read_only(database) as connection:
        connection.row_factory = sqlite3.Row
        row = connection.execute(
            f"SELECT * FROM {table} WHERE id = ?", (identifier,)
        ).fetchone()
    return None if row is None else dict(row)


def _read_only(database: Path):
    uri = database.resolve().as_uri() + "?mode=ro"
    return closing(sqlite3.connect(uri, uri=True))


def _public(
    row: dict[str, Any],
    source: str,
    scope: str,
    library_node: str,
) -> dict[str, object]:
    required = (
        "id", "path", "line_start", "line_end",
        "content", "source_sha256",
    )
    if any(name not in row for name in required):
        raise PeerQueryError("PEER_RESULT_INVALID")
    content = str(row["content"])
    if len(content) > MAX_CONTENT_CHARS:
        raise PeerQueryError("PEER_RESULT_TOO_LARGE")
    identifier = str(row["id"])
    if len(identifier) != 64 or set(identifier) - set("0123456789abcdef"):
        raw = f"{row['path']}:{row['line_start']}:{row['line_end']}"
        identifier = hashlib.sha256(raw.encode("utf-8")).hexdigest()
    return {
        "material_id": f"{source}:{identifier}",
        "library_node": library_node,
        "path": str(row["path"]),
        "line_start": int(row["line_start"]),
        "line_end": int(row["line_end"]),
        "content": content,
        "source_sha256": str(row["source_sha256"]),
        "scope": scope,
        "score": float(row.get("score", 0.0)),
    }


def _scope(library: PeerLibrary, source: str) -> str:
    if source == "project":
        return "peer-project-source"
    if source == "prefetch":
        return "peer-approved-cache"
    return f"peer-{library.kind}-descendant"


def _material_id(value: object) -> tuple[str, str]:
    if type(value) is not str:
        raise PeerQueryError("INVALID_MATERIAL_ID")
    parts = value.split(":", 1)
    if (
        len(parts) != 2
        or parts[0] not in {"project", "prefetch", "library"}
        or len(parts[1]) != 64
        or set(parts[1]) - set("0123456789abcdef")
    ):
        raise PeerQueryError("INVALID_MATERIAL_ID")
    return parts[0], parts[1]


def _request(project_id: object, query: object, limit: object) -> None:
    valid = type(project_id) is str and project_id.strip() == project_id
    valid = valid and 0 < len(project_id) <= 128
    if not valid:
        raise PeerQueryError("INVALID_PEER_PROJECT")
    if type(query) is not str or not query.strip() or len(query) > 2000:
        raise PeerQueryError("INVALID_PEER_QUERY")
    if type(limit) is not int or not 1 <= limit <= MAX_RESULTS:
        raise PeerQueryError("INVALID_PEER_LIMIT")


__all__ = [
    "PeerQueryError",
    "fetch_project_material",
    "fetch_subtree_material",
    "query_library_subtree",
    "query_project_library",
]
