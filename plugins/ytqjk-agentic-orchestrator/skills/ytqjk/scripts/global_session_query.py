from __future__ import annotations

import argparse
import hmac
import json
import os
from collections.abc import Callable
from pathlib import Path
from typing import Any

from file_lock import exclusive_file_lock
from global_store import is_current_approved_hit
from platform_paths import default_knowledge_root
from project_prefetch import (
    enforce_project_capacity,
    prefetch_stats,
    query_prefetch,
    sync_prefetch_generation,
    update_prefetch,
)
from project_source import project_query_state
from project_tracking import identify_project, track_project
from rag_common import (
    DEFAULT_CONFIG,
    SCHEMA_VERSION,
    atomic_json,
    config_fingerprint,
    lexical_query,
    load_json,
)
from rag_locks import global_lock, project_id_lock
from rag_query import build_vector_cache, query_vector_cache, vector_enabled
from session_memory import ensure_anchor


GLOBAL_CACHE = "global-cache"


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
    anchor, created = ensure_anchor(knowledge_root, session_id, project_id)
    track_project(knowledge_root, project_root, project)
    project_dir = knowledge_root / "projects" / project_id
    config = load_json(knowledge_root / "config.json", DEFAULT_CONFIG)
    with exclusive_file_lock(project_id_lock(knowledge_root, project_id)):
        project_manifest_path = project_dir / "manifest.json"
        project_manifest = load_json(project_manifest_path, {})
        project_database = project_dir / "lexical.sqlite3"
        if (
            project_database.is_file()
            and project_manifest.get("schema_version") != SCHEMA_VERSION
        ):
            raise RuntimeError("项目知识索引安全版本已过期，需要重新建立索引。")
        project_stale = _project_stale(project_root, project_manifest, config)
        project_results = _query_index(
            project_dir,
            project_database,
            project_manifest_path,
            project_manifest,
            knowledge_root,
            config,
            query,
            limit,
            "project-source-cache",
            allow_vector=not project_stale,
            project_cache=True,
        )
        if project_results:
            return _result(
                project_id, knowledge_root, project_manifest.get("indexed_at"),
                project_results, anchor, created, "PROJECT_CACHE_HIT",
                "current-project-cache-only", project_stale,
            )
        cached = query_prefetch(
            project_dir, query, limit, knowledge_root=knowledge_root
        )
        if cached:
            return _result(
                project_id, knowledge_root, cached[0].get("cached_at"), cached,
                anchor, created, "PROJECT_CACHE_HIT",
                "current-project-cache-only", project_stale,
            )

    cache = knowledge_root / GLOBAL_CACHE
    with exclusive_file_lock(global_lock(knowledge_root)):
        manifest_path = cache / "manifest.json"
        manifest = load_json(manifest_path, {})
        database = cache / "lexical.sqlite3"
        global_absent = not manifest and not database.is_file()
        if not global_absent and (
            manifest.get("schema_version") != SCHEMA_VERSION
            or not database.is_file()
        ):
            raise RuntimeError("全局知识索引不可用或已过期，需要重新建立索引。")
        if global_absent:
            generation = "GLOBAL_INDEX_ABSENT"
            indexed_at = None
        else:
            if manifest.get("config_fingerprint") != config_fingerprint(config):
                raise RuntimeError("全局知识索引配置已变化，需要重新建立索引。")
            generation = str(
                manifest.get("generation") or manifest.get("source_fingerprint") or ""
            )
            if not generation:
                raise RuntimeError("全局知识索引缺少代际信息，需要重新建立索引。")
            indexed_at = manifest.get("indexed_at")
        if global_absent:
            results: list[dict[str, Any]] = []
        else:
            results = _query_index(
                cache,
                database,
                manifest_path,
                manifest,
                knowledge_root,
                config,
                query,
                limit,
                "global-fallback",
                validator=lambda row: is_current_approved_hit(
                    knowledge_root, row
                ),
            )
        with exclusive_file_lock(project_id_lock(knowledge_root, project_id)):
            sync_prefetch_generation(project_dir, generation)
            if results:
                prefetched = update_prefetch(
                    project_dir, query, results, generation=generation
                )
            else:
                prefetched = []
    status = "GLOBAL_FALLBACK_HIT" if results else "KNOWLEDGE_MISS"
    scope = "global-fallback-current-project" if results else "no-knowledge"
    result = _result(
        project_id, knowledge_root, indexed_at, results,
        anchor, created, status, scope, project_stale,
    )
    result["prefetch_count"] = len(prefetched)
    if not results:
        result["next_action"] = "SEARCH_EXTERNAL_THEN_SUBMIT_CANDIDATE"
        result["intake_interface"] = "knowledge_intake_cli.py"
    return result


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
        mode = str(manifest.get("vector_mode", config.get("vector_mode", "auto")))
        stats = manifest.get("stats", {"text_bytes": 0, "chunks": 0})
        vector = manifest.get("vector", {})
        if (
            allow_vector
            and int(stats.get("chunks", 0)) > 0
            and vector_enabled(mode, stats, config)
        ):
            if not vector.get("enabled"):
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


def _project_stale(
    project_root: Path, manifest: dict[str, object], config: dict[str, Any]
) -> bool:
    indexed = manifest.get("indexed_identity")
    if not isinstance(indexed, dict):
        indexed = manifest.get("identity")
    if not isinstance(indexed, dict):
        return True
    current = project_query_state(project_root)
    if current["head"] == "NON_GIT":
        return True
    return (
        current["head"] != indexed.get("head")
        or current["dirty"] != "false"
        or indexed.get("dirty") not in {"false", "not-applicable"}
        or current.get("materialization") != indexed.get("materialization")
        or manifest.get("config_fingerprint") != config_fingerprint(config)
    )


def main() -> int:
    parser = argparse.ArgumentParser(description="Query global knowledge and anchor a Codex session.")
    parser.add_argument("query")
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--session-id", default=os.environ.get("CODEX_THREAD_ID", ""))
    parser.add_argument("--expected-project-id", required=True)
    parser.add_argument("--limit", type=int, default=5)
    args = parser.parse_args()
    try:
        if not args.session_id.strip():
            raise ValueError("宿主未提供稳定会话标识，无法创建会话锚点。")
        result = query_global(
            args.knowledge_root.resolve(), args.project_root.resolve(), args.query,
            args.session_id, args.expected_project_id, args.limit
        )
    except (OSError, RuntimeError, ValueError) as exc:
        result = {"ok": False, "error": str(exc)}
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
