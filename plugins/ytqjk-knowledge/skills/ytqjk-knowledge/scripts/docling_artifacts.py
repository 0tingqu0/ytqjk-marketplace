"""Stable Docling model inventory validation."""

from __future__ import annotations

import hashlib
import stat
from pathlib import Path

from scripts.artifact_safety import (
    ArtifactSafetyError,
    TreeGuard,
    snapshot_tree,
)


def is_link(path: Path) -> bool:
    try:
        attributes = path.lstat().st_file_attributes
    except (AttributeError, OSError):
        attributes = 0
    marker = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0)
    return path.is_symlink() or bool(attributes & marker)


def verified_artifacts(
    configured_root: Path,
    expected: dict[str, str],
    rapid: dict[str, str],
) -> tuple[Path, str, dict[str, Path], TreeGuard]:
    try:
        root = configured_root.resolve(strict=True)
        guard = snapshot_tree(root)
    except (OSError, ArtifactSafetyError) as error:
        raise ArtifactSafetyError("DOCLING_ARTIFACTS_UNSAFE") from error
    actual = {
        relative: digest
        for relative, digest in guard.hashes.items()
        if relative != "manifest.json"
    }
    if set(actual) != set(expected):
        raise ArtifactSafetyError("DOCLING_ARTIFACT_UNLISTED")
    if any(actual[name] != expected[name] for name in actual):
        raise ArtifactSafetyError("DOCLING_ARTIFACT_DIGEST")
    digest = hashlib.sha256()
    for relative, value in sorted(actual.items()):
        digest.update(relative.encode())
        digest.update(b"\0")
        digest.update(value.encode())
        digest.update(b"\n")
    paths = {key: root / value for key, value in rapid.items()}
    return root, digest.hexdigest(), paths, guard
