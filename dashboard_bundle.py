"""Materialize one immutable dashboard service bundle outside plugin cache."""

from __future__ import annotations

import os
import shutil
import uuid
from pathlib import Path

from codex_plugin_paths import (
    PluginPathError,
    prepare_codex_root,
    source_plugins,
    tree_hash,
)


PLUGIN_NAME = "ytqjk-agentic-orchestrator"
ASSETS = (
    ".codex-plugin/plugin.json",
    "skills/ytqjk/dashboard/dashboard_service.py",
    "skills/ytqjk/dashboard/index.html",
    "skills/ytqjk/scripts/document_runtime.py",
    "skills/ytqjk/scripts/requirements-document.txt",
)


class DashboardBundleError(RuntimeError):
    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


def materialize_dashboard_bundle(codex_root: Path) -> Path:
    """Copy the packaged service to a versioned, lifecycle-stable path."""
    try:
        source = next(
            item for item in source_plugins(_plugins_source())
            if item.name == PLUGIN_NAME
        )
        _safe_version(source.version)
        _require_assets(source.path)
        expected = tree_hash(source.path)
        root = prepare_codex_root(codex_root)
        parent = _prepare_parent(root)
        destination = parent / source.version
        if destination.exists() or destination.is_symlink():
            _verify_bundle(destination, expected)
            return destination
        stage = parent / f".stage-{uuid.uuid4().hex}"
        try:
            shutil.copytree(
                source.path,
                stage,
                ignore=shutil.ignore_patterns("__pycache__", "*.pyc"),
            )
            _verify_bundle(stage, expected)
            try:
                os.replace(stage, destination)
            except OSError:
                if not destination.exists():
                    raise
                _verify_bundle(destination, expected)
            _verify_bundle(destination, expected)
            return destination
        finally:
            _remove_stage(stage)
    except DashboardBundleError:
        raise
    except (OSError, PluginPathError, StopIteration) as error:
        raise DashboardBundleError("DASHBOARD_BUNDLE_FAILED") from error


def _plugins_source() -> Path:
    return Path(__file__).resolve().parent / "plugins"


def _prepare_parent(root: Path) -> Path:
    current = root
    for name in ("data", "ytqjk", "dashboard-service"):
        current = current / name
        if current.exists() or current.is_symlink():
            if not current.is_dir() or _link_or_reparse(current):
                raise DashboardBundleError("DASHBOARD_BUNDLE_UNSAFE")
            continue
        try:
            current.mkdir()
        except FileExistsError:
            if not current.is_dir() or _link_or_reparse(current):
                raise DashboardBundleError("DASHBOARD_BUNDLE_UNSAFE")
    return current


def _verify_bundle(root: Path, expected: str) -> None:
    if not root.is_dir() or _link_or_reparse(root):
        raise DashboardBundleError("DASHBOARD_BUNDLE_CONFLICT")
    _require_assets(root)
    if tree_hash(root) != expected:
        raise DashboardBundleError("DASHBOARD_BUNDLE_CONFLICT")


def _require_assets(root: Path) -> None:
    for relative in ASSETS:
        path = root / relative
        if not path.is_file() or _link_or_reparse(path):
            raise DashboardBundleError("PLUGIN_RUNTIME_ASSET_MISSING")


def _safe_version(version: str) -> None:
    allowed = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-"
    if not version or version in {".", ".."}:
        raise DashboardBundleError("PLUGIN_VERSION_INVALID")
    if any(character not in allowed for character in version):
        raise DashboardBundleError("PLUGIN_VERSION_INVALID")


def _remove_stage(stage: Path) -> None:
    if not stage.exists() and not stage.is_symlink():
        return
    if not stage.is_dir() or _link_or_reparse(stage):
        raise DashboardBundleError("DASHBOARD_BUNDLE_UNSAFE")
    shutil.rmtree(stage)


def _link_or_reparse(path: Path) -> bool:
    attributes = getattr(os.lstat(path), "st_file_attributes", 0)
    return path.is_symlink() or bool(attributes & 0x400)
