from __future__ import annotations

import io
import json
from contextlib import redirect_stdout
from pathlib import Path
from typing import Any, Callable

import pytest

from setup import main


ROOT = Path(__file__).resolve().parents[1]


def run_main(
    arguments: list[str],
    bootstrapper: Callable[
        [Path, Path, str], dict[str, object]
    ],
) -> tuple[int, dict[str, Any]]:
    output = io.StringIO()
    with redirect_stdout(output):
        code = main(
            arguments,
            project_bootstrapper=bootstrapper,
        )
    return code, json.loads(output.getvalue())


def forbidden_bootstrap(*args: object) -> dict[str, object]:
    raise AssertionError("project bootstrap must not run")


def test_target_root_is_never_used_as_project_root(
    tmp_path: Path,
) -> None:
    target = tmp_path / "install-target"
    code, receipt = run_main(
        [
            "--apply",
            "--yes",
            "--mode",
            "knowledge-only",
            "--target-root",
            str(target),
            "--codex-import",
            "off",
            "--json",
        ],
        forbidden_bootstrap,
    )

    assert code == 0
    assert receipt["knowledge_bootstrap"]["status"] == "NOT_CONFIGURED"
    assert (target / "skills" / "ytqjk-knowledge" / "SKILL.md").is_file()


def test_explicit_project_root_is_forwarded(
    tmp_path: Path,
) -> None:
    target = tmp_path / "install-target"
    project = tmp_path / "actual-project"
    knowledge = tmp_path / "knowledge"
    calls: list[tuple[Path, Path, str]] = []

    def bootstrap(
        knowledge_root: Path,
        project_root: Path,
        vector_mode: str,
    ) -> dict[str, object]:
        calls.append((knowledge_root, project_root, vector_mode))
        return {"status": "SUCCEEDED"}

    code, receipt = run_main(
        [
            "--apply",
            "--yes",
            "--mode",
            "knowledge-only",
            "--target-root",
            str(target),
            "--project-root",
            str(project),
            "--knowledge-root",
            str(knowledge),
            "--codex-import",
            "off",
            "--json",
        ],
        bootstrap,
    )

    assert code == 0
    assert receipt["knowledge_bootstrap"]["status"] == "SUCCEEDED"
    assert calls == [(knowledge, project, "auto")]


@pytest.mark.parametrize(
    ("case", "expected"),
    [
        ("dry-run", "SKIPPED_DRY_RUN"),
        ("off", "SKIPPED_OFF"),
        ("mode", "SKIPPED_MODE"),
        ("uninstall", "SKIPPED_UNINSTALL"),
    ],
)
def test_non_bootstrap_paths_never_call_project_bootstrap(
    tmp_path: Path,
    case: str,
    expected: str,
) -> None:
    target = tmp_path / "target"
    arguments = [
        "--mode",
        "knowledge-only",
        "--target-root",
        str(target),
        "--project-root",
        str(tmp_path / "project"),
        "--codex-import",
        "off",
        "--json",
    ]
    if case != "dry-run":
        arguments[0:0] = ["--apply", "--yes"]
    if case == "off":
        arguments.extend(["--project-bootstrap", "off"])
    elif case == "mode":
        grill = target / "skills" / "grill-me" / "SKILL.md"
        grill.parent.mkdir(parents=True)
        grill.write_text("fixture", encoding="utf-8")
        arguments[3] = "ide-only"
    elif case == "uninstall":
        arguments.append("--uninstall")

    code, receipt = run_main(arguments, forbidden_bootstrap)

    assert code == 0
    assert receipt["knowledge_bootstrap"]["status"] == expected


def test_default_launchers_bind_project_root_to_script_directory() -> None:
    powershell = (ROOT / "install.ps1").read_text(encoding="utf-8")
    shell = (ROOT / "install.sh").read_text(encoding="utf-8")

    assert "'--project-root', $PSScriptRoot" in powershell
    assert '--project-root "${script_dir}"' in shell
