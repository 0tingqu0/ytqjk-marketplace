"""Scoped removal of YTQJK artifacts installed by this distribution."""
from __future__ import annotations

import json
import os
import shutil
import time
from dataclasses import dataclass
from pathlib import Path

from install_core import contained_path, normalize_mode
from install_external import Action, InstallError, Runner
from install_external_codex import stage_managed_plugin_removal


@dataclass(frozen=True)
class UninstallPlan:
    mode: str
    actions: tuple[Action, ...]
    paths: tuple[Path, ...]

    @property
    def files(self) -> tuple[tuple[Path, Path], ...]:
        return ()


@dataclass
class TargetRemoval:
    backup: Path | None
    moves: list[tuple[Path, Path]]
    removed_paths: list[str]

    def rollback(self) -> None:
        for target, saved in reversed(self.moves):
            if saved.exists():
                os.replace(saved, target)
        self._cleanup()

    def finalize(self) -> bool:
        try:
            self._cleanup()
            return True
        except OSError:
            return False

    def _cleanup(self) -> None:
        if self.backup is not None and self.backup.exists():
            shutil.rmtree(self.backup)
        if self.backup is not None:
            parent = self.backup.parent
            if parent.exists() and not any(parent.iterdir()):
                parent.rmdir()


def uninstall_actions() -> tuple[Action, ...]:
    return (
        {
            "name": "plugin:orchestrator",
            "check": ["codex", "plugin", "list", "--json"],
            "identity": "ytqjk-agentic-orchestrator",
            "command": [
                "codex", "plugin", "remove", "ytqjk-agentic-orchestrator@ytqjk",
            ],
            "compensate": [
                "codex", "plugin", "add", "ytqjk-agentic-orchestrator@ytqjk",
            ],
        },
        {
            "name": "plugin:knowledge",
            "check": ["codex", "plugin", "list", "--json"],
            "identity": "ytqjk-knowledge",
            "command": ["codex", "plugin", "remove", "ytqjk-knowledge@ytqjk"],
            "compensate": [
                "codex", "plugin", "add", "ytqjk-knowledge@ytqjk",
            ],
        },
        {
            "name": "marketplace:ytqjk",
            "check": ["codex", "plugin", "marketplace", "list", "--json"],
            "identity": "ytqjk",
            "command": ["codex", "plugin", "marketplace", "remove", "ytqjk"],
            "compensate": [
                "codex", "plugin", "marketplace", "add",
                "0tingqu0/ytqjk-marketplace",
            ],
        },
    )


def uninstall_paths(mode: str, target: Path) -> tuple[Path, ...]:
    names: list[str] = []
    if mode in ("all", "ide-only"):
        names.extend(("ytqjk", "caveman"))
    if mode in ("all", "knowledge-only"):
        names.append("ytqjk-knowledge")
    return tuple(target / "skills" / name for name in names)


def build_uninstall_plan(mode: str, target: Path | None) -> UninstallPlan:
    mode = normalize_mode(mode)
    target = target or Path("<target-root>")
    actions = uninstall_actions() if mode in ("all", "codex-only") else ()
    return UninstallPlan(mode, actions, uninstall_paths(mode, target))


def _contains_state(text: str, identity: str) -> bool:
    try:
        value = json.loads(text)
    except json.JSONDecodeError as error:
        raise RuntimeError("state check returned invalid JSON") from error

    def contains(item: object) -> bool:
        if isinstance(item, str):
            return item == identity
        if isinstance(item, list):
            return any(contains(value) for value in item)
        if isinstance(item, dict):
            return (
                item.get("name") == identity
                or item.get("id") == identity
                or any(contains(value) for value in item.values())
            )
        return False

    return contains(value)


def _is_present(action: Action, runner: Runner, target: Path) -> bool:
    output = runner(list(action["check"]), target)
    return _contains_state(output, str(action["identity"]))


def stage_target_removal(plan: UninstallPlan, target: Path) -> TargetRemoval:
    paths = [
        contained_path(target, path)
        for path in uninstall_paths(plan.mode, target)
        if path.exists() or path.is_symlink()
    ]
    if not paths:
        return TargetRemoval(None, [], [])
    state = contained_path(target, target / ".ytqjk-uninstall")
    backup = contained_path(target, state / str(time.time_ns()))
    transaction = TargetRemoval(backup, [], [])
    try:
        backup.mkdir(parents=True)
        for path in paths:
            saved = backup / path.relative_to(target)
            saved.parent.mkdir(parents=True, exist_ok=True)
            os.replace(path, saved)
            transaction.moves.append((path, saved))
            transaction.removed_paths.append(path.relative_to(target).as_posix())
        return transaction
    except Exception:
        transaction.rollback()
        raise


def compensate(
    actions: list[Action], runner: Runner, target: Path
) -> tuple[str, ...]:
    failures: list[str] = []
    for action in reversed(actions):
        try:
            runner(list(action["compensate"]), target)
            if not _is_present(action, runner, target):
                raise RuntimeError("external artifact remains absent")
        except Exception:
            failures.append(str(action["name"]))
    return tuple(failures)


def apply_uninstall_plan(
    plan: UninstallPlan, target: Path, runner: Runner | None = None,
    codex_root: Path | None = None,
) -> dict[str, object]:
    target = target.resolve()
    if plan.actions and runner is None:
        raise InstallError(
            "external command runner is required", "NOT_APPLICABLE", "preflight"
        )

    stable = None
    try:
        stable = (
            stage_managed_plugin_removal(codex_root)
            if codex_root is not None and plan.mode in ("all", "codex-only")
            else None
        )
        local = stage_target_removal(plan, target)
    except Exception as error:
        if stable is not None:
            stable.rollback()
        raise InstallError(
            "uninstallation failed", "NOT_APPLICABLE", "local-preflight"
        ) from error

    commands: list[list[str]] = []
    removed_actions: list[Action] = []
    try:
        for action in plan.actions:
            if _is_present(action, runner, target):
                command = list(action["command"])
                runner(command, target)
                if _is_present(action, runner, target):
                    raise RuntimeError("external artifact remains installed")
                commands.append(command)
                removed_actions.append(action)
    except Exception as error:
        failures = compensate(removed_actions, runner, target)
        local.rollback()
        if stable is not None:
            stable.rollback()
        name = str(action.get("name", "codex-state-check"))
        raise InstallError(
            "uninstallation failed",
            "FAILED" if failures else "SUCCEEDED",
            name,
            failures,
        ) from error

    cleanup = local.finalize()
    if stable is not None:
        cleanup = stable.finalize() and cleanup
    removed = list(local.removed_paths)
    if stable is not None:
        removed.extend(stable.removed_paths)
    return {
        "status": "UNINSTALLED",
        "changed": bool(commands or removed),
        "external_commands": commands,
        "removed_paths": removed,
        "rollback": "NOT_APPLICABLE",
        "cleanup": "SUCCEEDED" if cleanup else "FAILED",
    }
