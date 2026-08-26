"""Transactional service boundary for local document runtime setup."""

from __future__ import annotations

import os
import shutil
import sys
import uuid
from pathlib import Path
from typing import Callable


SCRIPTS_DIR = Path(__file__).resolve().parent.parent / "scripts"
if str(SCRIPTS_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPTS_DIR))

from document_runtime import (  # noqa: E402
    DocumentRuntime,
    DocumentRuntimeError,
    inventory,
    prepare_directory,
    require_contained,
)
from document_runtime_lock import (  # noqa: E402
    RuntimeInstallLock,
    RuntimeLockError,
)
from path_safety import is_reparse  # noqa: E402


Runner = Callable[[list[str], int], object]
Downloader = Callable[[Path, Path], None]
RuntimePreparer = Callable[[Path, str], dict[str, object]]
DEFAULT_LOCK_TIMEOUT_SECONDS = 60.0


def check_document_runtime(
    root: Path,
    *,
    requirements: Path | None = None,
    runner: Runner | None = None,
    downloader: Downloader | None = None,
    platform_name: str | None = None,
) -> dict[str, object]:
    try:
        manager = _manager(
            root, requirements, runner, downloader, platform_name
        )
        return _check(manager)
    except DocumentRuntimeError as error:
        return _receipt("NOT_CONFIGURED", False, error.code, None)
    except Exception:
        return _receipt(
            "NOT_CONFIGURED", False, "RUNTIME_CHECK_FAILED", None
        )


def install_document_runtime(
    root: Path,
    *,
    requirements: Path | None = None,
    runner: Runner | None = None,
    downloader: Downloader | None = None,
    platform_name: str | None = None,
    lock_timeout_seconds: float = DEFAULT_LOCK_TIMEOUT_SECONDS,
) -> dict[str, object]:
    try:
        manager = _manager(
            root, requirements, runner, downloader, platform_name
        )
        prepare_directory(manager.root)
        prepare_directory(manager.runtime)
        lock_path = manager.runtime / ".install.lock"
        with RuntimeInstallLock(lock_path, lock_timeout_seconds):
            return _install_locked(manager)
    except RuntimeLockError as error:
        return _receipt("FAILED", False, error.code, None)
    except DocumentRuntimeError as error:
        return _receipt("FAILED", False, error.code, None)
    except Exception:
        return _receipt("FAILED", False, "INSTALL_FAILED", None)


def _install_locked(manager: DocumentRuntime) -> dict[str, object]:
    stage: Path | None = None
    try:
        current = _check(manager)
        if current["status"] == "READY":
            return current
        stage = _prepare_stage(manager)
        venv, models = _stage_targets(manager, stage)
        data = manager.build(venv, models)
        if not data:
            raise DocumentRuntimeError("STAGING_READBACK_FAILED")
        active = _activate(manager, venv, models)
        return _receipt("READY", True, None, active)
    finally:
        if stage is not None:
            try:
                _remove_tree(stage)
                if manager.platform == "win32":
                    stage.parent.rmdir()
            except (DocumentRuntimeError, OSError):
                pass


def prepare_document_runtime(
    root: Path,
    mode: str = "auto",
    *,
    requirements: Path | None = None,
    runner: Runner | None = None,
    downloader: Downloader | None = None,
    platform_name: str | None = None,
    lock_timeout_seconds: float = DEFAULT_LOCK_TIMEOUT_SECONDS,
) -> dict[str, object]:
    if mode == "off":
        return _receipt("SKIPPED", False, "NOT_CONFIGURED", None)
    if mode != "auto":
        return _receipt("FAILED", False, "INVALID_RUNTIME_MODE", None)
    checked = check_document_runtime(
        root,
        requirements=requirements,
        runner=runner,
        downloader=downloader,
        platform_name=platform_name,
    )
    if checked["status"] == "READY":
        return checked
    return install_document_runtime(
        root,
        requirements=requirements,
        runner=runner,
        downloader=downloader,
        platform_name=platform_name,
        lock_timeout_seconds=lock_timeout_seconds,
    )


def configured_document_runtime(
    root: Path,
    mode: str,
    preparer: RuntimePreparer | None = None,
) -> tuple[Path | None, dict[str, object]]:
    try:
        receipt = (preparer or prepare_document_runtime)(root, mode)
    except Exception:
        receipt = {
            "status": "FAILED",
            "runtime_status": "NOT_CONFIGURED",
            "reason": "RUNTIME_PREPARATION_FAILED",
            "python": None,
        }
    value = receipt.get("python")
    if mode == "auto" and receipt.get("status") == "READY":
        if type(value) is str and value:
            return Path(value), receipt
    return None, receipt


def dashboard_command(
    script: Path,
    root: Path,
    port: int,
    action: str,
    executable: Path | None = None,
    runtime_mode: str | None = None,
) -> list[str]:
    current = Path(executable or sys.executable).resolve()
    if sys.platform == "win32":
        windowless = current.with_name("pythonw.exe")
        if windowless.is_file():
            current = windowless
    command = [
        str(current), str(script.resolve()), action,
        "--knowledge-root", str(root), "--port", str(port),
    ]
    if runtime_mode is not None:
        command.extend(("--document-runtime", runtime_mode))
    return command


def _manager(
    root: Path,
    requirements: Path | None,
    runner: Runner | None,
    downloader: Downloader | None,
    platform_name: str | None,
) -> DocumentRuntime:
    return DocumentRuntime(
        root,
        requirements=requirements,
        runner=runner,
        downloader=downloader,
        platform_name=platform_name,
    )


def _check(manager: DocumentRuntime) -> dict[str, object]:
    try:
        data = manager.ready_data()
        return _receipt("READY", False, None, data)
    except DocumentRuntimeError as error:
        return _receipt("NOT_CONFIGURED", False, error.code, None)


def _prepare_stage(manager: DocumentRuntime) -> Path:
    prepare_directory(manager.root)
    prepare_directory(manager.runtime)
    prepare_directory(manager.models.parent)
    parent = manager.root if manager.platform == "win32" else manager.runtime
    folder = ".di" if manager.platform == "win32" else "install-staging"
    stage = parent / folder / uuid.uuid4().hex
    require_contained(stage, parent)
    prepare_directory(stage)
    return stage


def _stage_targets(
    manager: DocumentRuntime,
    stage: Path,
) -> tuple[Path, Path]:
    if manager.platform == "win32":
        return stage / "v", stage / "m"
    return stage / "venv", stage / "models"


def _activate(
    manager: DocumentRuntime,
    venv: Path,
    models: Path,
) -> dict[str, object]:
    token = uuid.uuid4().hex
    pairs = ((venv, manager.venv), (models, manager.models))
    old = {
        target: inventory(target) if target.exists() else None
        for _, target in pairs
    }
    backups: list[tuple[Path, Path]] = []
    installed: list[Path] = []
    try:
        for _, target in pairs:
            if target.exists():
                backup = target.with_name(f".{target.name}.{token}.bak")
                os.replace(target, backup)
                backups.append((target, backup))
        for source, target in pairs:
            os.replace(source, target)
            installed.append(target)
        data = manager.ready_data()
    except BaseException:
        _rollback(installed, backups, old)
        raise
    for _, backup in backups:
        _remove_tree(backup)
    return data


def _rollback(
    installed: list[Path],
    backups: list[tuple[Path, Path]],
    old: dict[Path, dict[str, str] | None],
) -> None:
    try:
        for target in reversed(installed):
            _remove_tree(target)
        for target, backup in reversed(backups):
            os.replace(backup, target)
        for target, expected in old.items():
            actual = inventory(target) if target.exists() else None
            if actual != expected:
                raise OSError("rollback readback mismatch")
    except Exception as error:
        raise DocumentRuntimeError("ROLLBACK_FAILED") from error


def _remove_tree(path: Path) -> None:
    if not path.exists():
        return
    if not path.is_dir() or is_reparse(path):
        raise DocumentRuntimeError("UNSAFE_RUNTIME_PATH")
    shutil.rmtree(path)


def _receipt(
    status: str,
    changed: bool,
    reason: str | None,
    data: dict[str, object] | None,
) -> dict[str, object]:
    runtime_status = "READY" if status == "READY" else "NOT_CONFIGURED"
    return {
        "schema_version": 1,
        "status": status,
        "runtime_status": runtime_status,
        "changed": changed,
        "reason": reason,
        "python": None if data is None else data["python"],
        "requirements_sha256": (
            None if data is None else data["requirements_sha256"]
        ),
        "packages": None if data is None else data["packages"],
        "models": None if data is None else data["models"],
    }
