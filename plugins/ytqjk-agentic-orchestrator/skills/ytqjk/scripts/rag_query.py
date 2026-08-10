from __future__ import annotations

import argparse
from pathlib import Path
from typing import Any

from global_store import chunks_fingerprint, scan_global
from rag_common import (
    DEFAULT_CONFIG,
    SCHEMA_VERSION,
    atomic_json,
    ensure_layout,
    lexical_query,
    load_json,
    read_chunks,
    utc_now,
)


GLOBAL_CACHE = "global-cache"


def vector_enabled(
    mode: str, stats: dict[str, int], config: dict[str, Any]
) -> bool:
    if mode == "on":
        return True
    if mode == "off":
        return False
    thresholds = config["auto"]
    return (
        stats["text_bytes"] >= int(thresholds["text_bytes"])
        or stats["chunks"] >= int(thresholds["chunks"])
    )


def build_vector_cache(
    project_dir: Path,
    knowledge_root: Path,
    config: dict[str, Any],
) -> dict[str, Any]:
    try:
        from vector_store import build_vectors

        embedding = config["embedding"]
        count = build_vectors(
            project_dir / "vectors",
            read_chunks(project_dir / "lexical.sqlite3"),
            str(embedding["model"]),
            int(embedding["dimensions"]),
            knowledge_root / "models",
        )
        return {"enabled": True, "status": "READY", "chunks": count, "error": None}
    except Exception as exc:
        return {
            "enabled": False,
            "status": "DEGRADED",
            "error": f"{type(exc).__name__}: {exc}",
        }


def reciprocal_rank_fusion(
    sources: list[tuple[str, list[dict[str, Any]]]], limit: int
) -> list[dict[str, Any]]:
    combined: dict[str, dict[str, Any]] = {}
    for source, rows in sources:
        for rank, row in enumerate(rows, start=1):
            chunk_id = f"{row.get('scope', 'unknown')}:{row['id']}"
            entry = combined.setdefault(
                chunk_id, {**row, "rrf_score": 0.0, "modes": []}
            )
            entry["rrf_score"] += 1.0 / (60 + rank)
            entry["modes"].append(source)
    return sorted(
        combined.values(), key=lambda item: item["rrf_score"], reverse=True
    )[:limit]


def _query_vectors(
    cache_dir: Path,
    query: str,
    config: dict[str, Any],
    knowledge_root: Path,
    limit: int,
) -> list[dict[str, Any]]:
    from vector_store import query_vectors

    embedding = config["embedding"]
    return query_vectors(
        cache_dir / "vectors",
        query,
        str(embedding["model"]),
        knowledge_root / "models",
        max(limit * 3, 20),
    )


def command_query(args: argparse.Namespace) -> dict[str, Any]:
    project_dir, identity = ensure_layout(args.knowledge_root, args.project_root)
    config = load_json(args.knowledge_root / "config.json", DEFAULT_CONFIG)
    manifest_path = project_dir / "manifest.json"
    manifest = load_json(manifest_path, {})
    database = project_dir / "lexical.sqlite3"
    if not database.exists():
        raise RuntimeError("Project cache is not indexed. Run index first.")
    if manifest.get("schema_version") != SCHEMA_VERSION:
        raise RuntimeError("Project cache security schema changed. Run index first.")
    indexed_identity = manifest.get("identity", {})
    stale = (
        indexed_identity.get("head") != identity["head"]
        or indexed_identity.get("source_fingerprint")
        != identity["source_fingerprint"]
    )
    lexical = lexical_query(database, args.query, max(args.limit * 3, 20))
    for row in lexical:
        row["scope"] = "project"

    global_dir = args.knowledge_root / GLOBAL_CACHE
    global_database = global_dir / "lexical.sqlite3"
    global_manifest_path = global_dir / "manifest.json"
    global_manifest = load_json(global_manifest_path, {})
    if (
        global_database.exists()
        and global_manifest.get("schema_version") != SCHEMA_VERSION
    ):
        raise RuntimeError(
            "Global cache security schema changed. Run index-global first."
        )
    global_stale = False
    if global_database.exists():
        current_chunks, _ = scan_global(args.knowledge_root, config)
        global_stale = global_manifest.get(
            "source_fingerprint"
        ) != chunks_fingerprint(current_chunks)
    global_lexical = (
        lexical_query(global_database, args.query, max(args.limit * 3, 20))
        if global_database.exists()
        else []
    )
    for row in global_lexical:
        row["scope"] = "global"

    misses = (
        0
        if lexical or global_lexical
        else int(manifest.get("low_confidence_queries", 0)) + 1
    )
    manifest["low_confidence_queries"] = misses
    vectors: list[dict[str, Any]] = []
    vector_info = manifest.get("vector", {})
    mode = str(manifest.get("vector_mode", config.get("vector_mode", "auto")))
    stats = manifest.get("stats", {"text_bytes": 0, "chunks": 0})
    should_enable = vector_enabled(mode, stats, config)
    if should_enable and not vector_info.get("enabled") and not stale:
        vector_info = build_vector_cache(project_dir, args.knowledge_root, config)
        manifest["vector"] = vector_info
    if vector_info.get("enabled"):
        try:
            vectors = _query_vectors(
                project_dir, args.query, config, args.knowledge_root, args.limit
            )
            for row in vectors:
                row["scope"] = "project"
        except Exception as exc:
            vector_info = {
                "enabled": False,
                "status": "DEGRADED",
                "error": f"{type(exc).__name__}: {exc}",
            }
            manifest["vector"] = vector_info

    global_vectors: list[dict[str, Any]] = []
    global_vector_info = global_manifest.get("vector", {})
    global_stats = global_manifest.get("stats", {"text_bytes": 0, "chunks": 0})
    global_mode = str(
        global_manifest.get("vector_mode", config.get("vector_mode", "auto"))
    )
    if (
        global_database.exists()
        and not global_stale
        and int(global_stats.get("chunks", 0)) > 0
        and vector_enabled(global_mode, global_stats, config)
        and not global_vector_info.get("enabled")
    ):
        global_vector_info = build_vector_cache(
            global_dir, args.knowledge_root, config
        )
        global_manifest["vector"] = global_vector_info
        atomic_json(global_manifest_path, global_manifest)
    if global_vector_info.get("enabled"):
        try:
            global_vectors = _query_vectors(
                global_dir, args.query, config, args.knowledge_root, args.limit
            )
            for row in global_vectors:
                row["scope"] = "global"
        except Exception as exc:
            global_vector_info = {
                "enabled": False,
                "status": "DEGRADED",
                "error": f"{type(exc).__name__}: {exc}",
            }
            global_manifest["vector"] = global_vector_info
            atomic_json(global_manifest_path, global_manifest)

    if lexical or global_lexical or vectors or global_vectors:
        misses = 0
        manifest["low_confidence_queries"] = 0
    manifest["last_query_at"] = utc_now()
    manifest["updated_at"] = utc_now()
    atomic_json(manifest_path, manifest)
    results = reciprocal_rank_fusion(
        [
            ("project-lexical", lexical),
            ("global-lexical", global_lexical),
            ("project-vector", vectors),
            ("global-vector", global_vectors),
        ],
        args.limit,
    )
    return {
        "project_dir": str(project_dir),
        "head": identity["head"],
        "stale": stale,
        "global_stale": global_stale,
        "vector": vector_info,
        "global_vector": global_vector_info,
        "low_confidence_queries": misses,
        "results": results,
    }
