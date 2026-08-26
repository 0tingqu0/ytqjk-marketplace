"""Document lookup helpers exported by the dashboard entry point."""

from __future__ import annotations

from pathlib import Path

from dashboard_snapshot import (
    global_index_library,
    project_library,
    snapshot as build_snapshot,
)
from stable_file import StableFileError, read_stable_bytes, snapshot_file


MAX_DOCUMENT_BYTES = 16 * 1024 * 1024


def safe_document(root: Path, raw_path: str) -> Path | None:
    root = root.absolute()
    candidate = (root / raw_path).absolute()
    try:
        candidate.relative_to(root)
    except ValueError:
        return None
    if candidate.suffix != ".md":
        return None
    try:
        snapshot_file(candidate, MAX_DOCUMENT_BYTES)
    except StableFileError:
        return None
    return candidate


def read_document(root: Path, raw_path: str) -> tuple[Path, str] | None:
    candidate = safe_document(root, raw_path)
    if candidate is None:
        return None
    try:
        _, content = read_stable_bytes(candidate, MAX_DOCUMENT_BYTES)
        return candidate, content.decode("utf-8")
    except (StableFileError, UnicodeDecodeError):
        return None


def snapshot(root: Path) -> dict[str, object]:
    return build_snapshot(root, safe_document)


__all__ = [
    "build_snapshot",
    "global_index_library",
    "project_library",
    "read_document",
    "safe_document",
    "snapshot",
]
