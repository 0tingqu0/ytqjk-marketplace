from __future__ import annotations

import argparse
from pathlib import Path
from typing import Any

from file_lock import exclusive_file_lock
from global_store import chunks_fingerprint, scan_global
from project_prefetch import sync_prefetch_generation
from rag_common import (
    DEFAULT_CONFIG,
    SCHEMA_VERSION,
    Chunk,
    atomic_json,
    build_lexical,
    config_fingerprint,
    load_json,
    utc_now,
)
from rag_locks import global_lock, project_id_lock
from rag_query import GLOBAL_CACHE, build_vector_cache, vector_enabled


def command_index_global(args: argparse.Namespace) -> dict[str, Any]:
    with exclusive_file_lock(global_lock(args.knowledge_root)):
        args.knowledge_root.mkdir(parents=True, exist_ok=True)
        config_path = args.knowledge_root / "config.json"
        if not config_path.exists():
            atomic_json(config_path, DEFAULT_CONFIG)
        config = load_json(config_path, DEFAULT_CONFIG)
        mode = args.vector_mode or config.get("vector_mode", "auto")
        validate_vector_mode(mode)
        global_dir = _ensure_global_layout(args.knowledge_root)
        chunks, stats = scan_global(args.knowledge_root, config)
        return _build_global_index(
            args, global_dir, chunks, stats, config, mode
        )


def bootstrap_global(
    args: argparse.Namespace, config: dict[str, Any], mode: str
) -> dict[str, Any]:
    with exclusive_file_lock(global_lock(args.knowledge_root)):
        global_dir = _ensure_global_layout(args.knowledge_root)
        manifest = load_json(global_dir / "manifest.json", {})
        chunks, stats = scan_global(args.knowledge_root, config)
        generation = chunks_fingerprint(chunks)
        if (
            (global_dir / "lexical.sqlite3").is_file()
            and manifest.get("schema_version") == SCHEMA_VERSION
            and manifest.get("vector_mode") == mode
            and manifest.get("config_fingerprint") == config_fingerprint(config)
            and (manifest.get("generation") or manifest.get("source_fingerprint"))
            == generation
        ):
            return {
                "global_dir": str(global_dir),
                "stats": manifest.get("stats", stats),
                "vector": manifest.get("vector", {}),
                "state": "REUSED",
                "generation": generation,
            }
        return _build_global_index(
            args,
            global_dir,
            chunks,
            stats,
            config,
            mode,
            build_vectors=False,
        )


def validate_vector_mode(mode: object) -> None:
    if mode not in {"off", "auto", "on"}:
        raise ValueError(f"Invalid vector mode: {mode}")


def _ensure_global_layout(knowledge_root: Path) -> Path:
    global_dir = knowledge_root / GLOBAL_CACHE
    global_dir.mkdir(parents=True, exist_ok=True)
    (global_dir / "vectors").mkdir(parents=True, exist_ok=True)
    return global_dir


def _build_global_index(
    args: argparse.Namespace,
    global_dir: Path,
    chunks: list[Chunk],
    stats: dict[str, int],
    config: dict[str, Any],
    mode: str,
    build_vectors: bool = True,
) -> dict[str, Any]:
    manifest_path = global_dir / "manifest.json"
    manifest = load_json(manifest_path, {"low_confidence_queries": 0})
    previous_generation = manifest.get("generation") or manifest.get(
        "source_fingerprint"
    )
    build_lexical(global_dir / "lexical.sqlite3", chunks)
    enabled = vector_enabled(mode, stats, config)
    vector_info = {"enabled": False, "status": "DISABLED", "error": None}
    if enabled and chunks and build_vectors:
        vector_info = build_vector_cache(global_dir, args.knowledge_root, config)
    elif enabled and chunks:
        vector_info["status"] = "PENDING_QUERY"
    generation = chunks_fingerprint(chunks)
    manifest.update(
        {
            "schema_version": SCHEMA_VERSION,
            "stats": stats,
            "source_fingerprint": generation,
            "generation": generation,
            "vector_mode": mode,
            "config_fingerprint": config_fingerprint(config),
            "vector": vector_info,
            "indexed_at": utc_now(),
            "updated_at": utc_now(),
        }
    )
    atomic_json(manifest_path, manifest)
    if previous_generation != generation:
        _invalidate_project_prefetch(args.knowledge_root, generation)
    return {
        "global_dir": str(global_dir),
        "stats": stats,
        "vector": vector_info,
        "state": "REBUILT",
        "generation": generation,
    }


def _invalidate_project_prefetch(
    knowledge_root: Path, generation: str
) -> None:
    projects = knowledge_root / "projects"
    if not projects.is_dir():
        return
    for project_dir in projects.iterdir():
        if not project_dir.is_dir():
            continue
        with exclusive_file_lock(
            project_id_lock(knowledge_root, project_dir.name)
        ):
            sync_prefetch_generation(project_dir, generation)
