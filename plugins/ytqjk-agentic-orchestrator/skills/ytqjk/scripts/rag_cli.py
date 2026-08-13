from __future__ import annotations

import argparse
import os
from pathlib import Path
from global_session_query import query_global
from json_output import write_json
from platform_paths import default_knowledge_root
from rag_index import (
    command_bootstrap,
    command_index,
    command_index_global,
    command_init,
)


def command_query(args: argparse.Namespace) -> dict[str, object]:
    if not args.session_id.strip():
        raise ValueError("查询必须提供稳定 --session-id 以绑定当前项目子库。")
    return query_global(
        args.knowledge_root,
        args.project_root,
        args.query,
        args.session_id,
        args.expected_project_id,
        args.limit,
    )


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
            sub.add_argument("--expected-project-id", required=True)
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
