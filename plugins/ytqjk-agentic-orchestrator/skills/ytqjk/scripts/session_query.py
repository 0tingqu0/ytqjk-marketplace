from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path

from platform_paths import default_knowledge_root
from session_memory import ensure_anchor


RAG_CLI = Path(__file__).with_name("rag_cli.py")
QUERY_TIMEOUT_SECONDS = 30


def anchor_query(root: Path, project_root: Path, session_id: str) -> dict[str, object]:
    project_id = project_root.resolve().name or "project"
    anchor, created = ensure_anchor(root, session_id, project_id)
    return {"session_key": anchor["session_key"], "created": created}


def main() -> int:
    parser = argparse.ArgumentParser(description="Query YTQJK knowledge with a session anchor.")
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--session-id", required=True)
    parser.add_argument("query")
    parser.add_argument("--limit", type=int, default=8)
    args = parser.parse_args()
    root, project = args.knowledge_root.resolve(), args.project_root.resolve()
    try:
        anchor_query(root, project, args.session_id)
    except (OSError, ValueError, RuntimeError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False))
        return 1
    command = [
        sys.executable,
        str(RAG_CLI),
        "--knowledge-root",
        str(root),
        "query",
        "--project-root",
        str(project),
        args.query,
        "--limit",
        str(args.limit),
    ]
    try:
        return subprocess.run(command, check=False, timeout=QUERY_TIMEOUT_SECONDS).returncode
    except subprocess.TimeoutExpired:
        print(json.dumps({"ok": False, "error": "知识库查询超时，请稍后重试。"}, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
