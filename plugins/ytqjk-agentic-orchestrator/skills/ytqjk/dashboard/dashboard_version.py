"""YTQJK release-version parsing independent of plugin SemVer."""
from __future__ import annotations

import json
from pathlib import Path


RELEASE_SCHEMA = "ytqjk-release-version/v1"


class VersionError(ValueError):
    """Raised when release or plugin version metadata is invalid."""


def numeric_version(value: object) -> str:
    if not isinstance(value, str):
        raise VersionError("version is not a string")
    parts = value.split(".")
    invalid = any(
        not part.isdigit() or len(part) > 1 and part.startswith("0")
        for part in parts
    )
    if len(parts) not in (3, 4) or invalid:
        raise VersionError("version must have three or four numeric parts")
    return value


def version_key(value: str) -> tuple[int, int, int, int]:
    parts = [int(part) for part in numeric_version(value).split(".")]
    return tuple(parts + [0] * (4 - len(parts)))


def plugin_version(value: object) -> str:
    version = numeric_version(value)
    if len(version.split(".")) != 3:
        raise VersionError("plugin version must use strict SemVer")
    return version


def installed_version(plugin_root: Path, expected_name: str) -> str:
    metadata = plugin_root / ".codex-plugin"
    manifest = _json(metadata / "plugin.json")
    if manifest.get("name") != expected_name:
        raise VersionError("plugin identity does not match")
    release_path = metadata / "release.json"
    if not release_path.is_file() or release_path.is_symlink():
        return plugin_version(manifest.get("version"))
    return release_version(release_path)


def release_version(path: Path) -> str:
    data = _json(path)
    if data.get("schema") != RELEASE_SCHEMA:
        raise VersionError("release schema does not match")
    return numeric_version(data.get("version"))


def _json(path: Path) -> dict[str, object]:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise VersionError("version metadata is invalid") from error
    if not isinstance(data, dict):
        raise VersionError("version metadata is not an object")
    return data
