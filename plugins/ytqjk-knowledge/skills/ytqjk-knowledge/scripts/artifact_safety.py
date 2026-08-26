"""Stable single-link snapshots for local inference artifacts."""

from __future__ import annotations

import hashlib
import os
import stat
from dataclasses import dataclass
from pathlib import Path


class ArtifactSafetyError(RuntimeError):
    """Artifact is linked, reparsed, replaced, or otherwise unstable."""


@dataclass(frozen=True, slots=True)
class FileGuard:
    path: Path
    identity: tuple[int, int]
    state: tuple[int, int, int]
    sha256: str


@dataclass(frozen=True, slots=True)
class TreeGuard:
    root: Path
    directories: tuple[tuple[Path, tuple[int, int], tuple[int, int]], ...]
    files: tuple[tuple[str, FileGuard], ...]

    @property
    def hashes(self) -> dict[str, str]:
        return {name: guard.sha256 for name, guard in self.files}


def read_bytes(path: Path, max_bytes: int) -> tuple[FileGuard, bytes]:
    guard, content = _capture(path, max_bytes, True)
    if content is None:
        raise ArtifactSafetyError("ARTIFACT_READ_FAILED")
    return guard, content


def snapshot_file(path: Path, max_bytes: int) -> FileGuard:
    return _capture(path, max_bytes, False)[0]


def verify_file(guard: FileGuard) -> None:
    _safe_chain(guard.path.parent)
    try:
        current = guard.path.lstat()
    except OSError as error:
        raise ArtifactSafetyError("ARTIFACT_CHANGED") from error
    _regular(current)
    if _identity(current) != guard.identity or _state(current) != guard.state:
        raise ArtifactSafetyError("ARTIFACT_CHANGED")


def snapshot_tree(
    root: Path,
    max_files: int = 100_000,
    max_bytes: int = 32 * 1024 * 1024 * 1024,
) -> TreeGuard:
    absolute = Path(root).absolute()
    _safe_chain(absolute)
    directories = []
    files = []
    total = 0
    try:
        for current, names, filenames in os.walk(
            absolute,
            topdown=True,
            followlinks=False,
        ):
            parent = Path(current).absolute()
            info = parent.lstat()
            _directory(info)
            directories.append(
                (parent, _identity(info), _directory_state(info))
            )
            names.sort()
            filenames.sort()
            for name in (*names, *filenames):
                if _is_reparse(parent / name):
                    raise ArtifactSafetyError("UNSAFE_REPARSE_PATH")
            for name in filenames:
                path = parent / name
                relative = path.relative_to(absolute).as_posix()
                guard = snapshot_file(path, max_bytes)
                total += guard.state[0]
                files.append((relative, guard))
                if len(files) > max_files or total > max_bytes:
                    raise ArtifactSafetyError("ARTIFACT_TREE_TOO_LARGE")
    except ArtifactSafetyError:
        raise
    except OSError as error:
        raise ArtifactSafetyError("ARTIFACT_TREE_UNAVAILABLE") from error
    if not directories:
        raise ArtifactSafetyError("ARTIFACT_TREE_UNAVAILABLE")
    result = TreeGuard(absolute, tuple(directories), tuple(files))
    verify_tree(result)
    return result


def verify_tree(guard: TreeGuard) -> None:
    _safe_chain(guard.root)
    for path, identity, state_value in guard.directories:
        try:
            info = path.lstat()
        except OSError as error:
            raise ArtifactSafetyError("ARTIFACT_TREE_CHANGED") from error
        _directory(info)
        if (
            _identity(info) != identity
            or _directory_state(info) != state_value
        ):
            raise ArtifactSafetyError("ARTIFACT_TREE_CHANGED")
    for _, item in guard.files:
        verify_file(item)
    expected_dirs = {
        path.relative_to(guard.root).as_posix()
        for path, _, _ in guard.directories
    }
    expected_files = {name for name, _ in guard.files}
    actual_dirs = set()
    actual_files = set()
    try:
        for current, names, filenames in os.walk(
            guard.root,
            topdown=True,
            followlinks=False,
        ):
            parent = Path(current).absolute()
            actual_dirs.add(parent.relative_to(guard.root).as_posix())
            for name in (*names, *filenames):
                if _is_reparse(parent / name):
                    raise ArtifactSafetyError("UNSAFE_REPARSE_PATH")
            actual_files.update(
                (parent / name).relative_to(guard.root).as_posix()
                for name in filenames
            )
    except ArtifactSafetyError:
        raise
    except OSError as error:
        raise ArtifactSafetyError("ARTIFACT_TREE_UNAVAILABLE") from error
    if actual_dirs != expected_dirs or actual_files != expected_files:
        raise ArtifactSafetyError("ARTIFACT_TREE_CHANGED")


def verify_files(guards: tuple[FileGuard, ...]) -> None:
    for guard in guards:
        verify_file(guard)


def _capture(
    path: Path,
    max_bytes: int,
    retain: bool,
) -> tuple[FileGuard, bytes | None]:
    absolute = Path(path).absolute()
    _safe_chain(absolute.parent)
    try:
        before = absolute.lstat()
        _regular(before)
        flags = os.O_RDONLY | getattr(os, "O_BINARY", 0)
        flags |= getattr(os, "O_NOFOLLOW", 0)
        descriptor = os.open(absolute, flags)
        with os.fdopen(descriptor, "rb") as stream:
            opened = os.fstat(stream.fileno())
            _regular(opened)
            digest = hashlib.sha256()
            chunks = [] if retain else None
            size = 0
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                size += len(chunk)
                if size > max_bytes:
                    raise ArtifactSafetyError("ARTIFACT_TOO_LARGE")
                digest.update(chunk)
                if chunks is not None:
                    chunks.append(chunk)
            after_open = os.fstat(stream.fileno())
        after_path = absolute.lstat()
    except ArtifactSafetyError:
        raise
    except OSError as error:
        raise ArtifactSafetyError("ARTIFACT_UNAVAILABLE") from error
    _safe_chain(absolute.parent)
    for info in (opened, after_open, after_path):
        _regular(info)
    path_changed = (
        _identity(after_path) != _identity(before)
        or _state(after_path) != _state(before)
    )
    handle_changed = (
        _identity(after_open) != _identity(opened)
        or _state(after_open) != _state(opened)
    )
    cross_api_changed = (
        _identity(opened) != _identity(before)
        or opened.st_size != before.st_size
        or opened.st_mtime_ns != before.st_mtime_ns
    )
    if path_changed or handle_changed or cross_api_changed:
        raise ArtifactSafetyError("ARTIFACT_CHANGED")
    content = b"".join(chunks) if chunks is not None else None
    guard = FileGuard(
        absolute,
        _identity(before),
        _state(before),
        digest.hexdigest(),
    )
    return guard, content


def _safe_chain(path: Path) -> None:
    for candidate in (path.absolute(), *path.absolute().parents):
        if _is_reparse(candidate):
            raise ArtifactSafetyError("UNSAFE_REPARSE_PATH")


def _is_reparse(path: Path) -> bool:
    try:
        info = path.lstat()
    except FileNotFoundError:
        return False
    except OSError:
        return True
    attributes = getattr(info, "st_file_attributes", 0)
    marker = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    return stat.S_ISLNK(info.st_mode) or bool(attributes & marker)


def _regular(info: os.stat_result) -> None:
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or not info.st_ino:
        raise ArtifactSafetyError("ARTIFACT_NOT_SINGLE_LINK")


def _directory(info: os.stat_result) -> None:
    if not stat.S_ISDIR(info.st_mode) or not info.st_ino:
        raise ArtifactSafetyError("UNSAFE_ARTIFACT_DIRECTORY")


def _identity(info: os.stat_result) -> tuple[int, int]:
    return info.st_dev, info.st_ino


def _state(info: os.stat_result) -> tuple[int, int, int]:
    return info.st_size, info.st_mtime_ns, info.st_ctime_ns


def _directory_state(info: os.stat_result) -> tuple[int, int]:
    return info.st_mtime_ns, info.st_ctime_ns
