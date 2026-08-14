"""Validated stable paths for the YTQJK Codex plugin copies."""
from __future__ import annotations

import hashlib
import json
import os
from dataclasses import dataclass
from pathlib import Path

PLUGIN_NAMES = (
    "ytqjk-agentic-orchestrator",
    "ytqjk-knowledge",
)
MANIFEST_NAME = ".ytqjk-managed.json"
MANIFEST_SCHEMA = "ytqjk-managed-plugins/v1"


class PluginPathError(RuntimeError):
    """Raised when a stable plugin path is not safe to manage."""


@dataclass(frozen=True)
class PluginSource:
    name: str
    version: str
    path: Path


def plugins_root(codex_root: Path) -> Path:
    return codex_root.expanduser().resolve() / "plugins"


def manifest_path(codex_root: Path) -> Path:
    return plugins_root(codex_root) / MANIFEST_NAME


def stable_path(codex_root: Path, name: str) -> Path:
    if name not in PLUGIN_NAMES:
        raise PluginPathError("unknown managed plugin")
    return plugins_root(codex_root) / name


def source_plugins(source_root: Path) -> tuple[PluginSource, ...]:
    root = source_root.resolve()
    if not root.is_dir() or root.is_symlink():
        raise PluginPathError("plugin source root is invalid")
    return tuple(_source_plugin(root, name) for name in PLUGIN_NAMES)


def _source_plugin(root: Path, name: str) -> PluginSource:
    path = root / name
    _inside(root, path, "plugin source escapes root")
    if not path.is_dir() or path.is_symlink():
        raise PluginPathError("plugin source is invalid")
    manifest = path / ".codex-plugin" / "plugin.json"
    if not manifest.is_file() or manifest.is_symlink():
        raise PluginPathError("plugin source manifest is invalid")
    try:
        data = json.loads(manifest.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise PluginPathError("plugin source manifest is invalid") from error
    version = data.get("version")
    if (
        data.get("name") != name
        or not isinstance(version, str)
        or not version
        or data.get("skills") != "./skills/"
    ):
        raise PluginPathError("plugin source manifest does not match plugin")
    skills = path / "skills"
    if not skills.is_dir() or skills.is_symlink():
        raise PluginPathError("plugin source skills are invalid")
    for item in path.rglob("*"):
        if item.is_symlink():
            raise PluginPathError("plugin source contains a symbolic link")
    return PluginSource(name, version, path)


def desired_manifest(sources: tuple[PluginSource, ...]) -> dict[str, object]:
    return {
        "schema": MANIFEST_SCHEMA,
        "plugins": [
            {
                "name": source.name,
                "version": source.version,
                "tree_sha256": tree_hash(source.path),
            }
            for source in sources
        ],
    }


def load_manifest(codex_root: Path) -> dict[str, object] | None:
    path = manifest_path(codex_root)
    if not path.exists() and not path.is_symlink():
        return None
    if not path.is_file() or path.is_symlink():
        raise PluginPathError("managed plugin manifest is invalid")
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise PluginPathError("managed plugin manifest is invalid") from error
    if not isinstance(data, dict):
        raise PluginPathError("managed plugin manifest is invalid")
    entries = data.get("plugins")
    names = (
        [entry.get("name") for entry in entries]
        if isinstance(entries, list) else []
    )
    if (
        data.get("schema") != MANIFEST_SCHEMA
        or len(entries or []) != len(PLUGIN_NAMES)
        or set(names) != set(PLUGIN_NAMES)
        or any(
            not isinstance(entry.get("version"), str)
            or not _sha256(entry.get("tree_sha256"))
            for entry in entries or []
        )
    ):
        raise PluginPathError("managed plugin manifest is invalid")
    return data


def validate_targets(codex_root: Path) -> dict[str, object] | None:
    root = plugins_root(codex_root)
    if root.is_symlink() or (root.exists() and not root.is_dir()):
        raise PluginPathError("stable plugin root is invalid")
    manifest = load_manifest(codex_root)
    for name in PLUGIN_NAMES:
        target = stable_path(codex_root, name)
        if not target.exists() and not target.is_symlink():
            continue
        if manifest is None:
            raise PluginPathError("stable plugin directory is not managed")
        if not target.is_dir() or target.is_symlink():
            raise PluginPathError("managed stable plugin directory is invalid")
    if manifest is not None:
        for entry in manifest["plugins"]:
            target = stable_path(codex_root, entry["name"])
            if not target.is_dir() or target.is_symlink():
                raise PluginPathError("managed stable plugin directory is invalid")
            if tree_hash(target) != entry["tree_sha256"]:
                raise PluginPathError("managed stable plugin directory was modified")
    return manifest


def tree_hash(root: Path) -> str:
    if not root.is_dir() or _link_or_reparse(root):
        raise PluginPathError("plugin tree contains a link or reparse point")
    digest = hashlib.sha256()
    _hash_directory(root, root, digest)
    return digest.hexdigest()


def _inside(root: Path, path: Path, message: str) -> None:
    try:
        path.resolve().relative_to(root)
    except ValueError as error:
        raise PluginPathError(message) from error


def _sha256(value: object) -> bool:
    return (
        isinstance(value, str)
        and len(value) == 64
        and all(character in "0123456789abcdef" for character in value)
    )


def _hash_directory(root: Path, directory: Path, digest) -> None:
    with os.scandir(directory) as entries:
        ordered = sorted(entries, key=lambda entry: entry.name)
    for entry in ordered:
        path = Path(entry.path)
        if _link_or_reparse(path):
            raise PluginPathError("plugin tree contains a link or reparse point")
        if entry.is_dir(follow_symlinks=False):
            if entry.name == "__pycache__":
                _validate_generated_cache(path)
            else:
                _hash_directory(root, path, digest)
        elif entry.is_file(follow_symlinks=False):
            digest.update(path.relative_to(root).as_posix().encode("utf-8"))
            digest.update(path.read_bytes())
        else:
            raise PluginPathError("plugin tree contains a non-regular entry")


def _validate_generated_cache(directory: Path) -> None:
    with os.scandir(directory) as entries:
        for entry in entries:
            path = Path(entry.path)
            if _link_or_reparse(path):
                raise PluginPathError(
                    "plugin tree contains a link or reparse point"
                )
            if not entry.is_file(follow_symlinks=False) or path.suffix != ".pyc":
                raise PluginPathError(
                    "plugin generated cache contains an unexpected entry"
                )


def _link_or_reparse(path: Path) -> bool:
    attributes = getattr(os.lstat(path), "st_file_attributes", 0)
    return path.is_symlink() or bool(attributes & 0x400)
