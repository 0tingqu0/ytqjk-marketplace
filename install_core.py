"""Dry-run-first installer planning and scoped standalone skill copying."""
from __future__ import annotations

import hashlib
import shutil
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

from install_external import (
    Action,
    CleanupResult,
    InstallError,
    Runner,
    apply_codex,
    cleanup_stage,
    codex_actions,
    grill_action,
    stage_grill,
)
from install_external_codex import materialize_plugins

VERSION = "0.4.5"
PUBLIC_MODES = ("all", "codex-only", "ide-only", "knowledge-only")
MODES = PUBLIC_MODES
Recovery = tuple[Path, Path | None, tuple[Path, ...]]


@dataclass(frozen=True)
class Plan:
    mode: str
    actions: tuple[Action, ...]
    files: tuple[tuple[Path, Path], ...]


def require_python() -> None:
    if sys.version_info < (3, 10):
        raise ValueError("Python 3.10+ is required.")


def normalize_mode(mode: str) -> str:
    if mode not in PUBLIC_MODES:
        raise ValueError("unsupported mode")
    return mode


def copy_sources(mode: str, target: Path) -> tuple[tuple[Path, Path], ...]:
    root = Path(__file__).resolve().parent
    plugin = root / "plugins" / "ytqjk-agentic-orchestrator"
    skill_root = plugin / "skills"
    sources: list[tuple[Path, Path]] = []
    if mode in ("all", "ide-only"):
        names = ("ytqjk", "caveman")
        sources.extend(
            (skill_root / name, target / "skills" / name)
            for name in names
        )
    if mode in ("all", "knowledge-only"):
        source = root / "plugins" / "ytqjk-knowledge" / "skills"
        sources.append((
            source / "ytqjk-knowledge",
            target / "skills" / "ytqjk-knowledge",
        ))
    return tuple(sources)


def build_plan(mode: str, target: Path | None) -> Plan:
    mode = normalize_mode(mode)
    requested_target = target
    target = target or Path("<target-root>")
    files = copy_sources(mode, target)
    actions: list[Action] = []
    if mode in ("all", "codex-only"):
        actions.extend(codex_actions())
    needs_grill = (
        mode in ("all", "ide-only")
        and (
            requested_target is None
            or not target_has_grill_me(requested_target)
        )
    )
    if needs_grill:
        actions.append(grill_action())
    return Plan(mode, tuple(actions), files)


def tree_hash(root: Path) -> str:
    digest = hashlib.sha256()
    for item in sorted(root.rglob("*")):
        if item.is_file():
            digest.update(item.relative_to(root).as_posix().encode())
            digest.update(item.read_bytes())
    return digest.hexdigest()


def remove_path(path: Path) -> None:
    if path.is_dir() and not path.is_symlink():
        shutil.rmtree(path)
    elif path.exists() or path.is_symlink():
        path.unlink()


def target_has_grill_me(target: Path) -> bool:
    return any(target.glob("**/grill-me/SKILL.md"))


def contained_path(target: Path, path: Path) -> Path:
    resolved = path.resolve()
    try:
        relative = resolved.relative_to(target)
    except ValueError as error:
        raise RuntimeError("installation path escapes target-root") from error
    if not relative.parts:
        raise RuntimeError("installation path must be inside target-root")
    return resolved


def resolved_files(plan: Plan, target: Path) -> tuple[tuple[Path, Path], ...]:
    return tuple(
        (source, contained_path(target, destination))
        for source, destination in copy_sources(plan.mode, target)
    )


def missing_parents(path: Path, target: Path) -> tuple[Path, ...]:
    missing: list[Path] = []
    current = path
    while current != target and not current.exists():
        missing.append(current)
        current = current.parent
    return tuple(missing)


def rollback_files(
    replacements: list[Recovery], snapshot: Path, state_dir: Path,
) -> tuple[str, ...]:
    failures: list[str] = []
    for index, item in enumerate(reversed(replacements), start=1):
        destination, backup, new_parents = item
        try:
            remove_path(destination)
            if backup is not None:
                destination.parent.mkdir(parents=True, exist_ok=True)
                if backup.is_dir():
                    shutil.copytree(backup, destination)
                else:
                    shutil.copy2(backup, destination)
            for parent in new_parents:
                if parent.exists() and not any(parent.iterdir()):
                    parent.rmdir()
        except Exception:
            failures.append(f"target-root-item:{index}")
    try:
        if snapshot.exists():
            shutil.rmtree(snapshot)
        parent = snapshot.parent
        if parent.exists() and not any(parent.iterdir()):
            parent.rmdir()
        if state_dir.exists() and not any(state_dir.iterdir()):
            state_dir.rmdir()
    except Exception:
        failures.append("target-root-snapshot")
    return tuple(failures)


def safe_cleanup(stage: object) -> CleanupResult:
    try:
        return cleanup_stage(stage)
    except Exception:
        return CleanupResult(
            "FAILED",
            True,
            "remove-target-root-staging-residue",
        )


def apply_plan(
    plan: Plan, target: Path, fail_after_copy: bool = False,
    runner: Runner | None = None,
    fail_on_copy: int | None = None,
    fault: Callable[[str, int, Path], None] | None = None,
    codex_root: Path | None = None,
) -> dict[str, object]:
    target = target.resolve()
    state_dir = contained_path(target, target / ".ytqjk-install")
    snapshots = contained_path(target, state_dir / "snapshots")
    snapshot = contained_path(target, snapshots / str(time.time_ns()))
    files = list(resolved_files(plan, target))
    replacements: list[Recovery] = []
    changed = False
    copied = 0
    executed: list[list[str]] = []
    materialized: dict[str, object] = {"changed": False, "stable_paths": []}
    codex = tuple(
        action for action in plan.actions if action.get("kind") == "codex"
    )
    needs_grill = any(
        action.get("kind") == "third-party-stage"
        for action in plan.actions
    )
    if (codex or needs_grill) and runner is None:
        raise InstallError(
            "external command runner is required", "NOT_NEEDED", "preflight"
        )
    stage = None
    failed_action = "target-root-files"
    try:
        if needs_grill:
            stage = stage_grill(target, runner)
            destination = contained_path(
                target, target / "skills" / "grill-me"
            )
            files.append((stage.source, destination))
        for source, destination in files:
            unchanged = (
                destination.is_dir()
                and tree_hash(source) == tree_hash(destination)
            )
            if unchanged:
                continue
            backup: Path | None = None
            if destination.exists():
                backup = snapshot / destination.relative_to(target)
                backup.parent.mkdir(parents=True, exist_ok=True)
                if destination.is_dir():
                    shutil.copytree(destination, backup)
                else:
                    shutil.copy2(destination, backup)
            new_parents = missing_parents(destination.parent, target)
            replacements.append((destination, backup, new_parents))
            remove_path(destination)
            index = copied + 1
            if fault:
                fault("before-mkdir", index, destination)
            destination.parent.mkdir(parents=True, exist_ok=True)
            if fault:
                fault("before-copy", index, destination)
            shutil.copytree(source, destination)
            changed = True
            copied += 1
            if fail_after_copy or copied == fail_on_copy:
                raise RuntimeError("injected copy failure")
        if codex:
            failed_action = "codex-actions"
            def materialize() -> None:
                nonlocal materialized
                if codex_root is not None:
                    materialized = materialize_plugins(codex_root, fault=fault)

            executed.extend(apply_codex(
                codex, runner, target, materialize, "codex-stable-paths"
            ))
    except Exception as error:
        failures = list(rollback_files(replacements, snapshot, state_dir))
        prior_status = "SUCCEEDED"
        cleanup = safe_cleanup(stage)
        if isinstance(error, InstallError):
            failures.extend(error.failed_compensations)
            failed_action = error.failed_action
            prior_status = error.rollback
            if error.cleanup != "NOT_NEEDED":
                cleanup = CleanupResult(
                    error.cleanup,
                    error.staging_residue,
                    error.cleanup_action,
                )
        if failures:
            status = "FAILED"
        elif replacements:
            status = "SUCCEEDED"
        else:
            status = prior_status
        raise InstallError(
            f"installation failed [{type(error).__name__}]",
            status,
            failed_action,
            tuple(failures),
            cleanup.status,
            cleanup.staging_residue,
            cleanup.action,
        ) from error
    cleanup = safe_cleanup(stage)
    return {
        "status": "APPLIED",
        "changed": changed or bool(executed) or bool(materialized["changed"]),
        "external_commands": executed,
        "snapshot": (
            snapshot.relative_to(target).as_posix()
            if snapshot.exists() else None
        ),
        "cleanup": cleanup.status,
        "staging_residue": cleanup.staging_residue,
        "cleanup_action": cleanup.action,
        "codex_plugins": materialized,
    }
