"""Managed global Codex guidance for automatic YTQJK knowledge discovery."""
from __future__ import annotations

import os
import shlex
import sys
import tempfile
from pathlib import Path

from codex_plugin_paths import stable_path


START = "<!-- ytqjk-knowledge managed:start -->"
END = "<!-- ytqjk-knowledge managed:end -->"
NAMES = ("AGENTS.md", "AGENTS.override.md")


def receipt(
    status: str, changed: bool = False, target: str | None = None
) -> dict[str, object]:
    result: dict[str, object] = {"status": status, "changed": changed}
    if target is not None:
        result["target"] = target
    return result


def configure(
    codex_root: Path,
    knowledge_root: Path,
    mode: str,
    action: str,
) -> dict[str, object]:
    if mode not in ("all", "codex-only"):
        return receipt("SKIPPED_MODE")
    try:
        if action == "install":
            return install(codex_root, knowledge_root)
        if action == "uninstall":
            return uninstall(codex_root)
        raise ValueError("unsupported guidance action")
    except (OSError, RuntimeError, ValueError):
        return receipt("FAILED")


def install(codex_root: Path, knowledge_root: Path) -> dict[str, object]:
    codex_root = codex_root.resolve()
    paths = [codex_root / name for name in NAMES]
    before = {path: _read(path) for path in paths}
    cleaned = {path: _remove_block(text) for path, text in before.items()}
    override = codex_root / "AGENTS.override.md"
    target = override if cleaned[override].strip() else codex_root / "AGENTS.md"
    block = _block(codex_root, knowledge_root.resolve())
    cleaned[target] = _append(cleaned[target], block)
    changed = _write_changes(before, cleaned)
    return receipt("INSTALLED", changed, target.name)


def uninstall(codex_root: Path) -> dict[str, object]:
    paths = [codex_root.resolve() / name for name in NAMES]
    before = {path: _read(path) for path in paths}
    after = {path: _remove_block(text) for path, text in before.items()}
    return receipt("REMOVED", _write_changes(before, after))


def _block(codex_root: Path, knowledge_root: Path) -> str:
    script = (
        stable_path(codex_root, "ytqjk-agentic-orchestrator")
        / "skills/ytqjk/scripts/session_query.py"
    )
    if not script.is_file():
        raise RuntimeError("managed knowledge query entrypoint is missing")
    values = (str(Path(sys.executable).resolve()), str(script), str(knowledge_root))
    if any("\n" in value or "\r" in value or "`" in value for value in values):
        raise ValueError("managed guidance path is unsafe")
    if os.name == "nt":
        command = (
            f"& {_ps(values[0])} {_ps(values[1])} '<task-related-query>' "
            f"--knowledge-root {_ps(values[2])} --project-root '<project-root>' "
            "--session-id $env:CODEX_THREAD_ID --limit 5"
        )
    else:
        command = (
            f"{shlex.quote(values[0])} {shlex.quote(values[1])} "
            f"'<task-related-query>' --knowledge-root {shlex.quote(values[2])} "
            "--project-root '<project-root>' --session-id "
            '"$CODEX_THREAD_ID" --limit 5'
        )
    return (
        f"{START}\n## YTQJK project knowledge\n\n"
        "- Before reading or changing project files, choose `<project-root>`: in a Git "
        "repository, resolve it with `git rev-parse --show-toplevel`; otherwise use the "
        "current working directory. A non-Git directory is valid and must not block "
        "knowledge queries.\n"
        "- When `CODEX_THREAD_ID` is available, run the following command with a query "
        "specific to the task. It registers the project and creates or refreshes one "
        "anonymous session anchor. Never invent or reuse a session ID.\n\n"
        f"  `{command}`\n\n"
        "- Report the returned `KNOWLEDGE_RECEIPT` in the first progress update or final "
        "answer. A miss is valid and must not be described as a knowledge hit.\n"
        f"{END}"
    )


def _ps(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def _read(path: Path) -> str:
    return path.read_text(encoding="utf-8") if path.is_file() else ""


def _remove_block(text: str) -> str:
    starts, ends = text.count(START), text.count(END)
    if starts != ends or starts > 1:
        raise ValueError("managed guidance markers are invalid")
    if not starts:
        return text
    left, remainder = text.split(START, 1)
    _, right = remainder.split(END, 1)
    cleaned = (left.rstrip() + "\n\n" + right.lstrip()).strip()
    return cleaned + ("\n" if cleaned else "")


def _append(text: str, block: str) -> str:
    return (text.rstrip() + "\n\n" if text.strip() else "") + block + "\n"


def _write_changes(before: dict[Path, str], after: dict[Path, str]) -> bool:
    changed = [path for path in before if before[path] != after[path]]
    written: list[Path] = []
    try:
        for path in changed:
            _atomic_write(path, after[path])
            written.append(path)
    except OSError:
        for path in reversed(written):
            _atomic_write(path, before[path])
        raise
    return bool(changed)


def _atomic_write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(dir=path.parent, suffix=".tmp")
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)
