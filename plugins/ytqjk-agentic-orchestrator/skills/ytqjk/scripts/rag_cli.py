from __future__ import annotations

import argparse
import os
from pathlib import Path
from typing import Any

from global_session_query import query_global
from global_store import chunks_fingerprint, scan_global
from json_output import write_json
from platform_paths import default_knowledge_root
from project_prefetch import enforce_project_capacity
from rag_common import (
    DEFAULT_CONFIG,
    SCHEMA_VERSION,
    atomic_json,
    build_lexical,
    ensure_layout,
    load_json,
    scan_project,
    utc_now,
)
from rag_query import GLOBAL_CACHE, build_vector_cache, vector_enabled


def command_init(args: argparse.Namespace) -> dict[str, Any]:
    project_dir, identity = ensure_layout(args.knowledge_root, args.project_root)
    manifest = {
        "schema_version": SCHEMA_VERSION,
        "identity": identity,
        "low_confidence_queries": 0,
        "vector": {"enabled": False, "status": "NOT_BUILT"},
        "created_at": utc_now(),
        "updated_at": utc_now(),
    }
    manifest_path = project_dir / "manifest.json"
    if manifest_path.exists():
        current = load_json(manifest_path, manifest)
        current["identity"] = identity
        current["updated_at"] = utc_now()
        manifest = current
    atomic_json(manifest_path, manifest)
    return {"project_dir": str(project_dir), "manifest": manifest}


def command_index(args: argparse.Namespace) -> dict[str, Any]:
    project_dir, identity = ensure_layout(args.knowledge_root, args.project_root)
    config = load_json(args.knowledge_root / "config.json", DEFAULT_CONFIG)
    manifest_path = project_dir / "manifest.json"
    manifest = load_json(manifest_path, {"low_confidence_queries": 0})
    mode = args.vector_mode or config.get("vector_mode", "auto")
    if mode not in {"off", "auto", "on"}:
        raise ValueError(f"Invalid vector mode: {mode}")
    chunks, stats = scan_project(args.project_root, config, identity["head"])
    database = project_dir / "lexical.sqlite3"
    build_lexical(database, chunks)
    enabled = vector_enabled(mode, stats, config)
    vector_info: dict[str, Any] = {"enabled": False, "status": "DISABLED", "error": None}
    if enabled:
        vector_info = build_vector_cache(project_dir, args.knowledge_root, config)
    manifest.update(
        {
            "schema_version": SCHEMA_VERSION,
            "identity": identity,
            "stats": stats,
            "vector_mode": mode,
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
    return {"project_dir": str(project_dir), "stats": stats, "vector": current["vector"]}


def command_index_global(args: argparse.Namespace) -> dict[str, Any]:
    args.knowledge_root.mkdir(parents=True, exist_ok=True)
    config_path = args.knowledge_root / "config.json"
    if not config_path.exists():
        atomic_json(config_path, DEFAULT_CONFIG)
    config = load_json(config_path, DEFAULT_CONFIG)
    global_dir = args.knowledge_root / GLOBAL_CACHE
    global_dir.mkdir(parents=True, exist_ok=True)
    (global_dir / "vectors").mkdir(parents=True, exist_ok=True)
    manifest_path = global_dir / "manifest.json"
    manifest = load_json(manifest_path, {"low_confidence_queries": 0})
    mode = args.vector_mode or config.get("vector_mode", "auto")
    chunks, stats = scan_global(args.knowledge_root, config)
    build_lexical(global_dir / "lexical.sqlite3", chunks)
    enabled = vector_enabled(mode, stats, config)
    vector_info = {"enabled": False, "status": "DISABLED", "error": None}
    if enabled and chunks:
        vector_info = build_vector_cache(global_dir, args.knowledge_root, config)
    manifest.update(
        {
            "schema_version": SCHEMA_VERSION,
            "stats": stats,
            "source_fingerprint": chunks_fingerprint(chunks),
            "vector_mode": mode,
            "vector": vector_info,
            "indexed_at": utc_now(),
            "updated_at": utc_now(),
        }
    )
    atomic_json(manifest_path, manifest)
    return {"global_dir": str(global_dir), "stats": stats, "vector": vector_info}


def command_query(args: argparse.Namespace) -> dict[str, object]:
    if not args.session_id.strip():
        raise ValueError("查询必须提供稳定 --session-id 以绑定当前项目子库。")
    return query_global(
        args.knowledge_root, args.project_root, args.query, args.session_id, args.limit
    )


def command_bootstrap(args: argparse.Namespace) -> dict[str, object]:
    initialized = command_init(args)
    project = command_index(args)
    global_index = command_index_global(args)
    return {
        "project_dir": initialized["project_dir"],
        "project": project,
        "global": global_index,
    }


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description="Local YTQJK agentic RAG cache.")
    result.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    subparsers = result.add_subparsers(dest="command", required=True)
    for name in ("init", "index", "query", "bootstrap"):
        sub = subparsers.add_parser(name)
        sub.add_argument("--project-root", type=Path, required=True)
        if name in {"index", "bootstrap"}:
            sub.add_argument("--vector-mode", choices=("off", "auto", "on"))
        if name == "query":
            sub.add_argument("query")
            sub.add_argument("--session-id", default=os.environ.get("CODEX_THREAD_ID", ""))
            sub.add_argument("--limit", type=int, default=8)
    global_index = subparsers.add_parser("index-global")
    global_index.add_argument("--vector-mode", choices=("off", "auto", "on"))
    return result


def main() -> int:
    args = parser().parse_args()
    args.knowledge_root = args.knowledge_root.resolve()
    if hasattr(args, "project_root"):
        args.project_root = args.project_root.resolve()
    commands = {
        "init": command_init,
        "index": command_index,
        "query": command_query,
        "bootstrap": command_bootstrap,
        "index-global": command_index_global,
    }
    try:
        output = commands[args.command](args)
    except (OSError, ValueError, RuntimeError) as exc:
        write_json({"ok": False, "error": str(exc)})
        return 1
    write_json({"ok": True, **output}, indent=2)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
