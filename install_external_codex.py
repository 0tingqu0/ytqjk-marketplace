"""Atomic stable-directory materialization for registered Codex plugins."""
from __future__ import annotations

import json
import os
import shutil
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

from codex_plugin_paths import (
    MANIFEST_NAME,
    PLUGIN_NAMES,
    PluginPathError,
    desired_manifest,
    load_manifest,
    manifest_path,
    plugins_root,
    source_plugins,
    stable_path,
    tree_hash,
    validate_targets,
)

Fault = Callable[[str, int, Path], None]


@dataclass
class ManagedPluginRemoval:
    """Reversible same-filesystem removal of manifest-owned plugin paths."""

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
        if self.backup is not None:
            _clean(self.backup)


def materialize_plugins(
    codex_root: Path, source_root: Path | None = None,
    fault: Fault | None = None,
) -> dict[str, object]:
    """Copy both packaged plugins into their manifest-owned stable paths."""
    sources = source_plugins(source_root or _source_root())
    codex_root = codex_root.expanduser().resolve()
    plugins = plugins_root(codex_root)
    manifest = validate_targets(codex_root)
    desired = desired_manifest(sources)
    updates = [
        source for source in sources
        if _needs_update(stable_path(codex_root, source.name), source.path)
    ]
    if not updates and manifest == desired:
        return _result(False)
    plugins.mkdir(parents=True, exist_ok=True)
    stage = plugins / f".ytqjk-stage-{time.time_ns()}"
    backup = plugins / f".ytqjk-backup-{time.time_ns()}"
    changes: list[tuple[Path, Path | None]] = []
    try:
        stage.mkdir()
        backup.mkdir()
        for source in updates:
            shutil.copytree(source.path, stage / source.name)
        staged_manifest = stage / MANIFEST_NAME
        staged_manifest.write_text(
            json.dumps(desired, ensure_ascii=False, sort_keys=True) + "\n",
            encoding="utf-8",
        )
        for index, source in enumerate(updates, start=1):
            target = stable_path(codex_root, source.name)
            if fault:
                fault("before-codex-plugin-replace", index, target)
            _replace(target, stage / source.name, backup, changes)
        _replace(manifest_path(codex_root), staged_manifest, backup, changes)
    except Exception as error:
        _restore(changes)
        _clean(stage)
        _clean(backup)
        raise PluginPathError("stable plugin materialization failed") from error
    _clean(stage)
    _clean(backup)
    return _result(True)


def stage_managed_plugin_removal(
    codex_root: Path, fault: Fault | None = None,
) -> ManagedPluginRemoval:
    """Move manifest-owned stable plugins aside before any CLI mutation."""
    codex_root = codex_root.expanduser().resolve()
    plugins = plugins_root(codex_root)
    if plugins.is_symlink() or (plugins.exists() and not plugins.is_dir()):
        raise PluginPathError("stable plugin root is invalid")
    manifest = load_manifest(codex_root)
    if manifest is None:
        return ManagedPluginRemoval(None, [], [])
    validate_targets(codex_root)
    backup = plugins / f".ytqjk-uninstall-{time.time_ns()}"
    transaction = ManagedPluginRemoval(backup, [], [])
    try:
        backup.mkdir()
        for index, entry in enumerate(manifest["plugins"], start=1):
            target = stable_path(codex_root, entry["name"])
            if fault:
                fault("before-codex-plugin-remove", index, target)
            saved = backup / target.name
            os.replace(target, saved)
            transaction.moves.append((target, saved))
            transaction.removed_paths.append(f"plugins/{target.name}")
        target = manifest_path(codex_root)
        os.replace(target, backup / MANIFEST_NAME)
        transaction.moves.append((target, backup / MANIFEST_NAME))
        return transaction
    except Exception as error:
        transaction.rollback()
        raise PluginPathError("stable plugin removal failed") from error


def _source_root() -> Path:
    return Path(__file__).resolve().parent / "plugins"


def _needs_update(target: Path, source: Path) -> bool:
    return not target.is_dir() or tree_hash(target) != tree_hash(source)


def _replace(
    target: Path, staged: Path, backup_root: Path,
    changes: list[tuple[Path, Path | None]],
) -> None:
    old = backup_root / target.name
    if target.exists():
        os.replace(target, old)
        changes.append((target, old))
    else:
        changes.append((target, None))
    os.replace(staged, target)


def _restore(changes: list[tuple[Path, Path | None]]) -> None:
    failures: list[Exception] = []
    for target, old in reversed(changes):
        try:
            _clean(target)
            if old is not None and old.exists():
                os.replace(old, target)
        except OSError as error:
            failures.append(error)
    if failures:
        raise PluginPathError("stable plugin rollback failed") from failures[0]


def _clean(path: Path) -> None:
    if path.is_dir() and not path.is_symlink():
        shutil.rmtree(path)
    elif path.exists() or path.is_symlink():
        path.unlink()


def _result(changed: bool) -> dict[str, object]:
    return {
        "changed": changed,
        "stable_paths": [f"plugins/{name}" for name in PLUGIN_NAMES],
    }
