from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

from platform_paths import default_knowledge_root
from project_prefetch import update_prefetch
from project_tracking import track_project
from rag_common import SCHEMA_VERSION, lexical_query, load_json
from session_memory import ensure_anchor


GLOBAL_CACHE = "global-cache"


def query_global(
    knowledge_root: Path, project_root: Path, query: str, session_id: str, limit: int
) -> dict[str, object]:
    project = track_project(knowledge_root, project_root)
    project_id = project["id"]
    anchor, created = ensure_anchor(knowledge_root, session_id, project_id)
    cache = knowledge_root / GLOBAL_CACHE
    manifest = load_json(cache / "manifest.json", {})
    database = cache / "lexical.sqlite3"
    if manifest.get("schema_version") != SCHEMA_VERSION or not database.is_file():
        raise RuntimeError("全局知识索引不可用或已过期，需要重新建立索引。")
    results = lexical_query(database, query, max(1, min(limit, 20)))
    prefetched = update_prefetch(knowledge_root / "projects" / project_id, query, results)
    return {
        "ok": True,
        "scope": "approved-global-read-only",
        "project_id": project_id,
        "project_tracking": "REGISTERED",
        "knowledge_root": str(knowledge_root),
        "indexed_at": manifest.get("indexed_at"),
        "stale": False,
        "result_count": len(results),
        "prefetch_count": len(prefetched),
        "results": results,
        "anchor_key": anchor["session_key"],
        "anchor_created": created,
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
