"""Scoped removal of YTQJK artifacts installed by this distribution."""
from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path

from install_core import contained_path, normalize_mode, remove_path
from install_external import Action, InstallError, Runner


@dataclass(frozen=True)
class UninstallPlan:
    mode: str
    actions: tuple[Action, ...]
    paths: tuple[Path, ...]

    @property
    def files(self) -> tuple[tuple[Path, Path], ...]:
        return ()


def uninstall_actions() -> tuple[Action, ...]:
    return (
        {
            "name": "plugin:orchestrator",
            "check": ["codex", "plugin", "list", "--json"],
            "identity": "ytqjk-agentic-orchestrator",
            "command": [
                "codex", "plugin", "remove", "ytqjk-agentic-orchestrator@ytqjk",
            ],
        },
        {
            "name": "plugin:knowledge",
            "check": ["codex", "plugin", "list", "--json"],
            "identity": "ytqjk-knowledge",
            "command": ["codex", "plugin", "remove", "ytqjk-knowledge@ytqjk"],
        },
        {
            "name": "marketplace:ytqjk",
            "check": ["codex", "plugin", "marketplace", "list", "--json"],
            "identity": "ytqjk",
            "command": ["codex", "plugin", "marketplace", "remove", "ytqjk"],
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


def apply_uninstall_plan(
    plan: UninstallPlan, target: Path, runner: Runner | None = None,
) -> dict[str, object]:
    target = target.resolve()
    if plan.actions and runner is None:
        raise InstallError(
            "external command runner is required", "NOT_APPLICABLE", "preflight"
        )

    commands: list[list[str]] = []
    try:
        for action in plan.actions:
            if _is_present(action, runner, target):
                command = list(action["command"])
                runner(command, target)
                if _is_present(action, runner, target):
                    raise RuntimeError("external artifact remains installed")
                commands.append(command)
    except Exception as error:
        name = str(action.get("name", "codex-state-check"))
        raise InstallError(
            "uninstallation failed", "NOT_APPLICABLE", name
        ) from error

    removed: list[str] = []
    try:
        for path in uninstall_paths(plan.mode, target):
            path = contained_path(target, path)
            if path.exists() or path.is_symlink():
                remove_path(path)
                removed.append(path.relative_to(target).as_posix())
    except Exception as error:
        raise InstallError(
            "uninstallation failed", "NOT_APPLICABLE", "target-root-files"
        ) from error
    return {
        "status": "UNINSTALLED",
        "changed": bool(commands or removed),
        "external_commands": commands,
        "removed_paths": removed,
        "rollback": "NOT_APPLICABLE",
    }
