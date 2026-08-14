"""Scoped third-party staging and compensating Codex operations."""
from __future__ import annotations

import hashlib
import json
import shutil
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

from install_external_grill import GRILL_COMMAND, grill_action

Action = dict[str, object]
Runner = Callable[[list[str], Path], str]


class InstallError(RuntimeError):
    def __init__(
        self,
        message: str,
        rollback: str,
        failed_action: str,
        failed_compensations: tuple[str, ...] = (),
        cleanup: str = "NOT_NEEDED",
        staging_residue: bool = False,
        cleanup_action: str | None = None,
    ) -> None:
        super().__init__(message)
        self.rollback = rollback
        self.failed_action = failed_action
        self.failed_compensations = failed_compensations
        self.cleanup = cleanup
        self.staging_residue = staging_residue
        self.cleanup_action = cleanup_action


@dataclass(frozen=True)
class StagedSkill:
    source: Path
    root: Path


@dataclass(frozen=True)
class CleanupResult:
    status: str
    staging_residue: bool
    action: str | None = None


def codex_action(
    name: str,
    check: list[str],
    identity: str,
    command: list[str],
    compensate: list[str],
) -> Action:
    prefix = ["codex", "plugin"]
    return {
        "kind": "codex",
        "name": name,
        "check": prefix + check,
        "identity": identity,
        "command": prefix + command,
        "compensate": prefix + compensate,
    }


def codex_actions() -> tuple[Action, ...]:
    return (
        codex_action(
            "marketplace:ytqjk",
            ["marketplace", "list", "--json"],
            "ytqjk",
            ["marketplace", "add", "0tingqu0/ytqjk-marketplace"],
            ["marketplace", "remove", "ytqjk"],
        ),
        codex_action(
            "plugin:orchestrator",
            ["list", "--json"],
            "ytqjk-agentic-orchestrator",
            ["add", "ytqjk-agentic-orchestrator@ytqjk"],
            ["remove", "ytqjk-agentic-orchestrator@ytqjk"],
        ),
        codex_action(
            "plugin:knowledge",
            ["list", "--json"],
            "ytqjk-knowledge",
            ["add", "ytqjk-knowledge@ytqjk"],
            ["remove", "ytqjk-knowledge@ytqjk"],
        ),
    )


def tree_state(root: Path) -> dict[str, tuple[str, str]]:
    state: dict[str, tuple[str, str]] = {}
    if not root.exists():
        return state
    for item in sorted(root.rglob("*")):
        relative = item.relative_to(root).as_posix()
        if item.is_symlink():
            state[relative] = ("link", str(item.resolve()))
        elif item.is_dir():
            state[relative] = ("dir", "")
        elif item.is_file():
            digest = hashlib.sha256(item.read_bytes()).hexdigest()
            state[relative] = ("file", digest)
    return state


def _inside(root: Path, path: Path) -> bool:
    try:
        path.resolve().relative_to(root.resolve())
        return True
    except ValueError:
        return False


def _validate_staging(root: Path) -> Path:
    for item in root.rglob("*"):
        if not _inside(root, item):
            raise InstallError(
                "staged output escapes target-root",
                "SUCCEEDED",
                "skill:grill-me",
            )
    matches = [
        item.parent
        for item in root.rglob("SKILL.md")
        if item.parent.name == "grill-me"
    ]
    if len(matches) != 1 or not _inside(root, matches[0]):
        raise InstallError(
            "staged grill-me output is invalid",
            "SUCCEEDED",
            "skill:grill-me",
        )
    return matches[0]


def _restore_skills(
    skills: Path, backup: Path, baseline: dict[str, tuple[str, str]]
) -> None:
    if tree_state(skills) == baseline:
        return
    try:
        if skills.exists() or skills.is_symlink():
            if skills.is_dir() and not skills.is_symlink():
                shutil.rmtree(skills)
            else:
                skills.unlink()
        if backup.exists():
            shutil.copytree(backup, skills, symlinks=True)
        if tree_state(skills) != baseline:
            raise RuntimeError("restored target does not match snapshot")
    except Exception as error:
        raise InstallError(
            "third-party target rollback failed",
            "FAILED",
            "skill:grill-me",
            ("target-root-skills",),
        ) from error


def stage_grill(target: Path, runner: Runner) -> StagedSkill:
    state_root = target / ".ytqjk-install" / "staging"
    root = state_root / str(time.time_ns())
    work = root / "work"
    skills = target / "skills"
    backup = root / "backup"
    baseline = tree_state(skills)
    stage = StagedSkill(work, root)
    target_may_need_restore = False
    try:
        work.mkdir(parents=True)
        if skills.exists():
            shutil.copytree(skills, backup, symlinks=True)
        target_may_need_restore = True
        runner(list(GRILL_COMMAND), work)
        if tree_state(skills) != baseline:
            raise RuntimeError(
                "third-party command changed target before promotion",
            )
        return StagedSkill(_validate_staging(work), root)
    except Exception as error:
        restore_error = None
        if target_may_need_restore:
            try:
                _restore_skills(skills, backup, baseline)
            except InstallError as caught:
                restore_error = caught
        cleanup = cleanup_stage(stage)
        if restore_error is not None:
            raise InstallError(
                "third-party staging failed",
                "FAILED",
                "skill:grill-me",
                restore_error.failed_compensations,
                cleanup.status,
                cleanup.staging_residue,
                cleanup.action,
            ) from error
        if isinstance(error, InstallError):
            raise InstallError(
                str(error),
                error.rollback,
                error.failed_action,
                error.failed_compensations,
                cleanup.status,
                cleanup.staging_residue,
                cleanup.action,
            ) from error
        raise InstallError(
            "third-party staging failed",
            "SUCCEEDED" if target_may_need_restore else "NOT_NEEDED",
            "skill:grill-me",
            cleanup=cleanup.status,
            staging_residue=cleanup.staging_residue,
            cleanup_action=cleanup.action,
        ) from error


def cleanup_stage(stage: StagedSkill | None) -> CleanupResult:
    if stage is None:
        return CleanupResult("NOT_NEEDED", False)
    try:
        if stage.root.exists():
            shutil.rmtree(stage.root)
        staging = stage.root.parent
        if staging.exists() and not any(staging.iterdir()):
            staging.rmdir()
        state = staging.parent
        if state.exists() and not any(state.iterdir()):
            state.rmdir()
        return CleanupResult("SUCCEEDED", False)
    except Exception:
        return CleanupResult(
            "FAILED",
            True,
            "remove-target-root-staging-residue",
        )


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
            direct = item.get("name") == identity or item.get("id") == identity
            return direct or any(contains(value) for value in item.values())
        return False

    return contains(value)


def _present(action: Action, runner: Runner, cwd: Path) -> bool:
    output = runner(list(action["check"]), cwd)
    return _contains_state(output, str(action["identity"]))


def _compensate(
    actions: list[Action], runner: Runner, cwd: Path
) -> tuple[str, ...]:
    failures: list[str] = []
    for action in reversed(actions):
        try:
            runner(list(action["compensate"]), cwd)
            if _present(action, runner, cwd):
                raise RuntimeError("compensation state remains present")
        except Exception:
            failures.append(str(action["name"]))
    return tuple(failures)


def apply_codex(
    actions: tuple[Action, ...], runner: Runner, cwd: Path,
    after_apply: Callable[[], None] | None = None,
    after_name: str = "codex-materialization",
) -> list[list[str]]:
    existing: list[tuple[Action, bool]] = []
    try:
        for action in actions:
            existing.append((action, _present(action, runner, cwd)))
    except Exception as error:
        name = str(action.get("name", "codex-state-check"))
        raise InstallError(
            "external state check failed", "NOT_NEEDED", name
        ) from error

    created: list[Action] = []
    executed: list[list[str]] = []
    failed_action = "codex-preflight"
    uncertain: list[str] = []
    try:
        for action, preexisting in existing:
            if preexisting:
                continue
            failed_action = str(action["name"])
            command = list(action["command"])
            try:
                runner(command, cwd)
            except Exception:
                try:
                    if _present(action, runner, cwd):
                        created.append(action)
                except Exception:
                    uncertain.append(f"verify:{failed_action}")
                raise
            created.append(action)
            executed.append(command)
            if not _present(action, runner, cwd):
                uncertain.append(f"verify:{failed_action}")
                raise RuntimeError("external action did not create state")
        if after_apply is not None:
            failed_action = after_name
            after_apply()
    except Exception as error:
        failures = list(_compensate(created, runner, cwd))
        failures.extend(uncertain)
        rollback = "FAILED" if failures else "SUCCEEDED"
        raise InstallError(
            "installation failed",
            rollback,
            failed_action,
            tuple(failures),
        ) from error
    return executed
