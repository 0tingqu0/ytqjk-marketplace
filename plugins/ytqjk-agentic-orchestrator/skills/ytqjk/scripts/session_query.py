from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path

from platform_paths import default_knowledge_root


QUERY_CLI = Path(__file__).with_name("global_session_query.py")
QUERY_TIMEOUT_SECONDS = 60


def main() -> int:
    parser = argparse.ArgumentParser(description="Query YTQJK knowledge with a session anchor.")
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--session-id", required=True)
    parser.add_argument("--expected-project-id", required=True)
    parser.add_argument("query")
    parser.add_argument("--limit", type=int, default=8)
    args = parser.parse_args()
    root, project = args.knowledge_root.resolve(), args.project_root.resolve()
    command = [
        sys.executable,
        str(QUERY_CLI),
        args.query,
        "--knowledge-root", str(root),
        "--project-root", str(project),
        "--session-id", args.session_id,
        "--expected-project-id", args.expected_project_id,
        "--limit",
        str(args.limit),
    ]
    try:
        return subprocess.run(command, check=False, timeout=QUERY_TIMEOUT_SECONDS).returncode
    except subprocess.TimeoutExpired:
        print(json.dumps({
            "ok": False,
            "error": "知识库查询超时，请稍后重试。",
            "retryable": True,
            "timeout_seconds": QUERY_TIMEOUT_SECONDS,
        }, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
