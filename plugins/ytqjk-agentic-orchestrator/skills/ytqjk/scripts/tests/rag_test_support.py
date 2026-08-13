from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
RAG = SCRIPTS / "rag_cli.py"
sys.path.insert(0, str(SCRIPTS))

from project_tracking import identify_project  # noqa: E402


def git(repo: Path, *args: str) -> None:
    subprocess.run(
        ["git", "-C", str(repo), *args], check=True, capture_output=True
    )


def make_repo(path: Path, content: str = "seed") -> Path:
    path.mkdir()
    git(path, "init")
    git(path, "config", "user.email", "test@example.com")
    git(path, "config", "user.name", "Test")
    (path / "guide.md").write_text(content, encoding="utf-8")
    git(path, "add", "guide.md")
    git(path, "commit", "-m", "seed")
    return path


def run_rag(
    knowledge: Path,
    command: str,
    project: Path | None = None,
    *args: str,
    expect_success: bool = True,
) -> dict[str, object]:
    invocation = [
        sys.executable,
        str(RAG),
        "--knowledge-root",
        str(knowledge),
        command,
    ]
    if project is not None:
        invocation.extend(["--project-root", str(project)])
    invocation.extend(args)
    result = subprocess.run(
        invocation, check=False, capture_output=True, encoding="utf-8"
    )
    if expect_success and result.returncode != 0:
        raise AssertionError(result.stderr or result.stdout)
    return json.loads(result.stdout)


def query(
    knowledge: Path, project: Path, session: str, text: str
) -> dict[str, object]:
    return run_rag(
        knowledge,
        "query",
        project,
        "--session-id",
        session,
        "--expected-project-id",
        identify_project(project)["id"],
        text,
    )


def index_global(knowledge: Path) -> dict[str, object]:
    return run_rag(
        knowledge, "index-global", None, "--vector-mode", "off"
    )
