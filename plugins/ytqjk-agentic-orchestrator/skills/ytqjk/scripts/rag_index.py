from __future__ import annotations

import argparse
from pathlib import Path
from typing import Any

from file_lock import exclusive_file_lock
from global_store import chunks_fingerprint
from project_prefetch import enforce_project_capacity
from project_source import project_query_state
from rag_global_index import bootstrap_global, command_index_global, validate_vector_mode
from rag_locks import project_lock
from rag_common import (
    DEFAULT_CONFIG,
    SCHEMA_VERSION,
    Chunk,
    atomic_json,
    build_lexical,
    config_fingerprint,
    ensure_layout,
    load_json,
    scan_project,
    utc_now,
)
from rag_query import build_vector_cache, vector_enabled


def command_init(args: argparse.Namespace) -> dict[str, Any]:
    with exclusive_file_lock(project_lock(args.knowledge_root, args.project_root)):
        return _initialize(args)


def _initialize(args: argparse.Namespace) -> dict[str, Any]:
    project_dir, stable = ensure_layout(args.knowledge_root, args.project_root)
    identity = {**stable, **project_query_state(args.project_root)}
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "identity": identity,
        "indexed_identity": None,
        "low_confidence_queries": 0,
        "vector": {"enabled": False, "status": "NOT_BUILT"},
        "created_at": utc_now(),
        "updated_at": utc_now(),
    }
    manifest_path = project_dir / "manifest.json"
    if manifest_path.exists():
        current = load_json(manifest_path, manifest)
        if (
            not current.get("indexed_identity")
            and (project_dir / "lexical.sqlite3").is_file()
            and current.get("identity", {}).get("source_fingerprint")
        ):
            current["indexed_identity"] = current["identity"]
        current["identity"] = identity
        current["updated_at"] = utc_now()
        manifest = current
    atomic_json(manifest_path, manifest)
    return {"project_dir": str(project_dir), "manifest": manifest}


def command_index(args: argparse.Namespace) -> dict[str, Any]:
    with exclusive_file_lock(project_lock(args.knowledge_root, args.project_root)):
        project_dir, stable = ensure_layout(args.knowledge_root, args.project_root)
        config = load_json(args.knowledge_root / "config.json", DEFAULT_CONFIG)
        mode = args.vector_mode or config.get("vector_mode", "auto")
        validate_vector_mode(mode)
        source_root = Path(stable["root"])
        state = project_query_state(source_root)
        identity = {**stable, **state}
        chunks, stats = scan_project(
            source_root, config, state["head"], args.knowledge_root
        )
        return _build_project_index(
            args, project_dir, identity, state, chunks, stats, config, mode
        )


def _build_project_index(
    args: argparse.Namespace,
    project_dir: Path,
    identity: dict[str, str],
    state: dict[str, str],
    chunks: list[Chunk],
    stats: dict[str, int],
    config: dict[str, Any],
    mode: str,
    build_vectors: bool = True,
) -> dict[str, Any]:
    manifest_path = project_dir / "manifest.json"
    manifest = load_json(manifest_path, {"low_confidence_queries": 0})
    build_lexical(project_dir / "lexical.sqlite3", chunks)
    enabled = vector_enabled(mode, stats, config)
    vector_info: dict[str, Any] = {
        "enabled": False,
        "status": "DISABLED",
        "error": None,
    }
    if enabled and build_vectors:
        vector_info = build_vector_cache(project_dir, args.knowledge_root, config)
    elif enabled:
        vector_info["status"] = "PENDING_QUERY"
    indexed_identity = {
        **identity,
        **state,
        "source_fingerprint": chunks_fingerprint(chunks),
    }
    manifest.update(
        {
            "schema_version": SCHEMA_VERSION,
            "identity": identity,
            "indexed_identity": indexed_identity,
            "stats": stats,
            "vector_mode": mode,
            "config_fingerprint": config_fingerprint(config),
            "vector": vector_info,
            "indexed_at": utc_now(),
            "updated_at": utc_now(),
        }
    )
    atomic_json(manifest_path, manifest)
    evicted = enforce_project_capacity(project_dir)
    if "lexical.sqlite3" in evicted:
        raise RuntimeError("项目索引超过 1 GiB，已丢弃本次可重建索引。")
    current = load_json(manifest_path, manifest)
    return {
        "project_dir": str(project_dir),
        "stats": stats,
        "vector": current["vector"],
        "state": "REBUILT",
        "source_fingerprint": indexed_identity["source_fingerprint"],
    }


def command_bootstrap(args: argparse.Namespace) -> dict[str, object]:
    with exclusive_file_lock(project_lock(args.knowledge_root, args.project_root)):
        initialized = _initialize(args)
        project_dir = Path(str(initialized["project_dir"]))
        identity = dict(initialized["manifest"]["identity"])
        config = load_json(args.knowledge_root / "config.json", DEFAULT_CONFIG)
        requested = args.vector_mode or config.get("vector_mode", "auto")
        validate_vector_mode(requested)
        project = _bootstrap_project(
            args, project_dir, identity, config, requested
        )
    global_index = bootstrap_global(args, config, requested)
    return {
        "project_dir": initialized["project_dir"],
        "project": project,
        "global": global_index,
        "vector_mode": requested,
    }


def _bootstrap_project(
    args: argparse.Namespace,
    project_dir: Path,
    identity: dict[str, str],
    config: dict[str, Any],
    mode: str,
) -> dict[str, Any]:
    manifest_path = project_dir / "manifest.json"
    manifest = load_json(manifest_path, {})
    indexed = manifest.get("indexed_identity") or {}
    source_root = Path(identity["root"])
    state = project_query_state(source_root)
    reusable = (
        (project_dir / "lexical.sqlite3").is_file()
        and manifest.get("schema_version") == SCHEMA_VERSION
        and manifest.get("vector_mode") == mode
        and manifest.get("config_fingerprint") == config_fingerprint(config)
        and state["head"] != "NON_GIT"
        and state["dirty"] == "false"
        and indexed.get("head") == state["head"]
        and indexed.get("dirty") == "false"
        and indexed.get("materialization") == state.get("materialization")
    )
    if reusable:
        return _project_reused(project_dir, manifest, "REUSED")
    chunks, stats = scan_project(
        source_root, config, state["head"], args.knowledge_root
    )
    fingerprint = chunks_fingerprint(chunks)
    if (
        (project_dir / "lexical.sqlite3").is_file()
        and manifest.get("schema_version") == SCHEMA_VERSION
        and manifest.get("vector_mode") == mode
        and manifest.get("config_fingerprint") == config_fingerprint(config)
        and indexed.get("source_fingerprint") == fingerprint
    ):
        manifest["identity"] = identity
        manifest["indexed_identity"] = {
            **identity,
            **state,
            "source_fingerprint": fingerprint,
        }
        manifest["verified_at"] = utc_now()
        manifest["updated_at"] = utc_now()
        atomic_json(manifest_path, manifest)
        return _project_reused(project_dir, manifest, "REUSED_VERIFIED")
    return _build_project_index(
        args,
        project_dir,
        identity,
        state,
        chunks,
        stats,
        config,
        mode,
        build_vectors=False,
    )


def _project_reused(
    project_dir: Path, manifest: dict[str, Any], state: str
) -> dict[str, Any]:
    return {
        "project_dir": str(project_dir),
        "stats": manifest.get("stats", {}),
        "vector": manifest.get("vector", {}),
        "state": state,
        "source_fingerprint": (manifest.get("indexed_identity") or {}).get(
            "source_fingerprint"
        ),
    }
