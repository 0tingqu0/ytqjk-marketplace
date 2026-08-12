from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

from platform_paths import default_knowledge_root
from project_prefetch import prefetch_stats, query_prefetch, update_prefetch
from project_tracking import identify_project, track_project
from rag_common import SCHEMA_VERSION, lexical_query, load_json
from session_memory import ensure_anchor


GLOBAL_CACHE = "global-cache"


def query_global(
    knowledge_root: Path, project_root: Path, query: str, session_id: str, limit: int
) -> dict[str, object]:
    if not query.strip():
        raise ValueError("知识检索问题不能为空。")
    project = identify_project(project_root)
    project_id = project["id"]
    anchor, created = ensure_anchor(knowledge_root, session_id, project_id)
    track_project(knowledge_root, project_root, project)
    project_dir = knowledge_root / "projects" / project_id
    cached = query_prefetch(project_dir, query, limit)
    if cached:
        return _result(
            project_id, knowledge_root, cached[0].get("cached_at"), cached, anchor, created,
            "PROJECT_CACHE_HIT", "current-project-cache-only",
        )
    project_database = project_dir / "lexical.sqlite3"
    project_manifest = load_json(project_dir / "manifest.json", {})
    if (
        project_database.is_file()
        and project_manifest.get("schema_version") != SCHEMA_VERSION
    ):
        raise RuntimeError("项目知识索引安全版本已过期，需要重新建立索引。")
    project_results = (
        lexical_query(project_database, query, max(1, min(limit, 20)))
        if project_database.is_file()
        and project_manifest.get("schema_version") == SCHEMA_VERSION
        else []
    )
    if project_results:
        for row in project_results:
            row["scope"] = "project-source-cache"
        return _result(
            project_id, knowledge_root, project_manifest.get("indexed_at"),
            project_results, anchor, created, "PROJECT_CACHE_HIT",
            "current-project-cache-only",
        )
    cache = knowledge_root / GLOBAL_CACHE
    manifest = load_json(cache / "manifest.json", {})
    database = cache / "lexical.sqlite3"
    global_absent = not manifest and not database.is_file()
    if not global_absent and (
        manifest.get("schema_version") != SCHEMA_VERSION or not database.is_file()
    ):
        raise RuntimeError("全局知识索引不可用或已过期，需要重新建立索引。")
    results = (
        [] if global_absent
        else lexical_query(database, query, max(1, min(limit, 20)))
    )
    for row in results:
        row["scope"] = "global-fallback"
    prefetched = update_prefetch(project_dir, query, results) if results else []
    status = "GLOBAL_FALLBACK_HIT" if results else "KNOWLEDGE_MISS"
    scope = "global-fallback-current-project" if results else "no-knowledge"
    result = _result(
        project_id, knowledge_root, manifest.get("indexed_at"), results,
        anchor, created, status, scope,
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
) -> dict[str, object]:
    return {
        "ok": True,
        "status": status,
        "scope": scope,
        "project_id": project_id,
        "project_tracking": "REGISTERED",
        "knowledge_root": str(knowledge_root),
        "indexed_at": indexed_at,
        "stale": False,
        "result_count": len(results),
        "results": results,
        "anchor_key": anchor["session_key"],
        "anchor_created": created,
        "cache": prefetch_stats(knowledge_root / "projects" / project_id),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Query global knowledge and anchor a Codex session.")
    parser.add_argument("query")
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--session-id", default=os.environ.get("CODEX_THREAD_ID", ""))
    parser.add_argument("--limit", type=int, default=5)
    args = parser.parse_args()
    try:
        if not args.session_id.strip():
            raise ValueError("宿主未提供稳定会话标识，无法创建会话锚点。")
        result = query_global(
            args.knowledge_root.resolve(), args.project_root.resolve(), args.query, args.session_id, args.limit
        )
    except (OSError, RuntimeError, ValueError) as exc:
        result = {"ok": False, "error": str(exc)}
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
