from __future__ import annotations

import argparse
import hmac
import json
import os
from collections.abc import Callable
from pathlib import Path
from typing import Any

from file_lock import exclusive_file_lock
from knowledge_tree_runtime import (
    QueryTree,
    add_tree_evidence,
    bootstrap_query_tree,
    capture_bootstrap_intent,
    query_node,
    query_tree_transaction,
    unavailable_node,
)
from platform_paths import default_knowledge_root
from project_prefetch import (
    enforce_project_capacity,
    prefetch_stats,
    sync_prefetch_generation,
)
from project_tracking import identify_project, track_project
from query_provenance import (
    cache_ancestor_results,
    current_governed_results,
    project_stale as project_index_stale,
)
from rag_common import (
    DEFAULT_CONFIG,
    atomic_json,
    lexical_query,
    load_json,
)
from rag_locks import project_id_lock
from rag_query import build_vector_cache, query_vector_cache, vector_enabled
from session_memory import ensure_anchor, validate_session_binding


def query_global(
    knowledge_root: Path,
    project_root: Path,
    query: str,
    session_id: str,
    expected_project_id: str,
    limit: int,
) -> dict[str, object]:
    if not query.strip():
        raise ValueError("知识检索问题不能为空。")
    if not expected_project_id.strip():
        raise ValueError("知识检索必须提供请求方项目标识。")
    project = identify_project(project_root)
    project_id = project["id"]
    if not hmac.compare_digest(project_id, expected_project_id):
        raise ValueError(
            f"请求方项目标识与工作目录不匹配：expected={expected_project_id}。"
        )
    validate_session_binding(knowledge_root, session_id, project_id)
    bootstrap_intent = capture_bootstrap_intent(knowledge_root, project_id)
    track_project(knowledge_root, project_root, project)
    query_tree = bootstrap_query_tree(
        knowledge_root, project_id, bootstrap_intent
    )
    with query_tree_transaction(knowledge_root, query_tree):
        return _query_locked(
            knowledge_root,
            project_root,
            query,
            session_id,
            project_id,
            limit,
            query_tree,
        )


def _query_locked(
    knowledge_root: Path,
    project_root: Path,
    query: str,
    session_id: str,
    project_id: str,
    limit: int,
    query_tree: QueryTree,
) -> dict[str, object]:
    anchor, created = ensure_anchor(knowledge_root, session_id, project_id)
    project_dir = knowledge_root / "projects" / project_id
    chain = "/".join(node.node_id for node in query_tree.chain)
    cache_generation = "tree-chain:" + chain
    if query_tree.revision > 0:
        with exclusive_file_lock(project_id_lock(knowledge_root, project_id)):
            sync_prefetch_generation(project_dir, cache_generation)
    config = load_json(knowledge_root / "config.json", DEFAULT_CONFIG)
    project_stale = True
    visited: list[str] = []
    unavailable: list[dict[str, str]] = []
    last_indexed_at: object = None
    for position, node in enumerate(query_tree.chain):
        visited.append(node.node_id)
        outcome = query_node(
            node, position == 0, knowledge_root, project_root,
            config, query, limit,
            query_index=_query_index,
            project_stale=project_index_stale,
            request_project_id=project_id,
        )
        if position == 0:
            project_stale = bool(outcome["stale"])
        reason = outcome.get("unavailable_reason")
        if isinstance(reason, str):
            unavailable.append(unavailable_node(node, reason))
            continue
        last_indexed_at = outcome.get("indexed_at")
        results = outcome["results"]
        if not isinstance(results, list) or not results:
            continue
        peer_hit = node.kind == "mounted"
        if not peer_hit:
            results = current_governed_results(
                knowledge_root, results, position > 0
            )
        if not results:
            continue
        prefetched = []
        if position > 0 and not peer_hit:
            results, prefetched = cache_ancestor_results(
                knowledge_root,
                project_dir,
                project_id,
                query,
                results,
                cache_generation,
            )
            if not results:
                continue
        status = "PROJECT_CACHE_HIT"
        if peer_hit:
            status = "PEER_FALLBACK_HIT"
        elif position > 0:
            status = "GLOBAL_FALLBACK_HIT"
        if peer_hit:
            scope = "peer-same-project-mounted"
        elif position == 0:
            scope = "current-project-cache-only"
        else:
            scope = "tree-ancestor-fallback-current-project"
        result = _result(
            project_id, knowledge_root, outcome.get("indexed_at"),
            results, anchor, created, status, scope, project_stale,
        )
        result["prefetch_count"] = len(prefetched)
        return add_tree_evidence(
            result, query_tree, visited, unavailable, node.node_id,
        )
    result = _result(
        project_id, knowledge_root, last_indexed_at, [], anchor, created,
        "KNOWLEDGE_MISS", "no-knowledge", project_stale,
    )
    result["prefetch_count"] = 0
    result["next_action"] = "SEARCH_EXTERNAL_THEN_SUBMIT_CANDIDATE"
    result["intake_interface"] = "knowledge_intake_cli.py"
    return add_tree_evidence(
        result, query_tree, visited, unavailable, None,
    )


def _result(
    project_id: str,
    knowledge_root: Path,
    indexed_at: object,
    results: list[dict[str, object]],
    anchor: dict[str, object],
    created: bool,
    status: str,
    scope: str,
    stale: bool,
) -> dict[str, object]:
    return {
        "ok": True,
        "status": status,
        "scope": scope,
        "project_id": project_id,
        "project_tracking": "REGISTERED",
        "knowledge_root": str(knowledge_root),
        "indexed_at": indexed_at,
        "stale": stale,
        "result_count": len(results),
        "results": results,
        "anchor_key": anchor["session_key"],
        "anchor_created": created,
        "cache": prefetch_stats(knowledge_root / "projects" / project_id),
    }


def _query_index(
    cache_dir: Path,
    database: Path,
    manifest_path: Path,
    manifest: dict[str, Any],
    knowledge_root: Path,
    config: dict[str, Any],
    query: str,
    limit: int,
    scope: str,
    allow_vector: bool = True,
    project_cache: bool = False,
    validator: Callable[[dict[str, Any]], bool] | None = None,
    read_only: bool = False,
) -> list[dict[str, Any]]:
    if not database.is_file():
        return []
    bounded_limit = max(1, min(limit, 20))
    page_size = max(bounded_limit * 3, 20)
    results: list[dict[str, Any]] = []
    offset = 0
    while len(results) < bounded_limit:
        page = lexical_query(database, query, page_size, offset=offset)
        if validator is None:
            results.extend(page)
        else:
            results.extend(row for row in page if validator(row))
        if len(page) < page_size:
            break
        offset += len(page)
    if not results:
        mode = str(
            manifest.get("vector_mode", config.get("vector_mode", "auto"))
        )
        stats = manifest.get("stats", {"text_bytes": 0, "chunks": 0})
        vector = manifest.get("vector", {})
        if (
            allow_vector
            and int(stats.get("chunks", 0)) > 0
            and vector_enabled(mode, stats, config)
        ):
            if not vector.get("enabled") and not read_only:
                vector = build_vector_cache(cache_dir, knowledge_root, config)
                manifest["vector"] = vector
                atomic_json(manifest_path, manifest)
                if project_cache:
                    enforce_project_capacity(cache_dir)
                    current = load_json(manifest_path, manifest)
                    manifest.clear()
                    manifest.update(current)
                    vector = manifest.get("vector", {})
            if vector.get("enabled"):
                try:
                    results = query_vector_cache(
                        cache_dir, query, config, knowledge_root, limit
                    )
                    if validator is not None:
                        results = [row for row in results if validator(row)]
                except Exception as exc:
                    if read_only:
                        raise RuntimeError(
                            "ANCESTOR_VECTOR_QUERY_FAILED"
                        ) from exc
                    manifest["vector"] = {
                        "enabled": False,
                        "status": "DEGRADED",
                        "error": f"{type(exc).__name__}: {exc}",
                    }
                    atomic_json(manifest_path, manifest)
    results = results[:bounded_limit]
    for row in results:
        row["scope"] = scope
    return results


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Query global knowledge and anchor a Codex session."
    )
    parser.add_argument("query")
    parser.add_argument(
        "--knowledge-root",
        type=Path,
        default=default_knowledge_root(),
    )
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument(
        "--session-id",
        default=os.environ.get("CODEX_THREAD_ID", ""),
    )
    parser.add_argument("--expected-project-id", required=True)
    parser.add_argument("--limit", type=int, default=5)
    args = parser.parse_args()
    try:
        if not args.session_id.strip():
            raise ValueError("宿主未提供稳定会话标识，无法创建会话锚点。")
        result = query_global(
            args.knowledge_root.resolve(),
            args.project_root.resolve(),
            args.query,
            args.session_id,
            args.expected_project_id,
            args.limit,
        )
    except (OSError, RuntimeError, ValueError) as exc:
        result = {"ok": False, "error": str(exc)}
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
