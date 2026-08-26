"""Stable single-link snapshots for security-sensitive local files."""

from __future__ import annotations

import hashlib
import os
import stat
from dataclasses import dataclass
from pathlib import Path

from path_safety import is_reparse


class StableFileError(RuntimeError):
    """A file or directory changed, escaped, or is multiply linked."""


@dataclass(frozen=True, slots=True)
class FileSnapshot:
    path: Path
    identity: tuple[int, int]
    state: tuple[int, int, int]
    sha256: str


@dataclass(frozen=True, slots=True)
class DirectorySnapshot:
    path: Path
    identity: tuple[int, int]
    state: tuple[int, int]


@dataclass(frozen=True, slots=True)
class TreeSnapshot:
    root: Path
    directories: tuple[DirectorySnapshot, ...]
    files: tuple[tuple[str, FileSnapshot], ...]
    total_bytes: int
    max_files: int
    max_bytes: int
    excluded: frozenset[str]

    @property
    def hashes(self) -> dict[str, str]:
        return {
            relative: snapshot.sha256
            for relative, snapshot in self.files
        }


def read_stable_bytes(
    path: Path,
    max_bytes: int,
) -> tuple[FileSnapshot, bytes]:
    if type(max_bytes) is not int or max_bytes < 1:
        raise StableFileError("INVALID_FILE_LIMIT")
    snapshot, content = _capture_file(Path(path), max_bytes, True)
    if content is None:
        raise StableFileError("FILE_READ_FAILED")
    return snapshot, content


def snapshot_file(
    path: Path,
    max_bytes: int | None = None,
) -> FileSnapshot:
    if max_bytes is not None and (
        type(max_bytes) is not int or max_bytes < 1
    ):
        raise StableFileError("INVALID_FILE_LIMIT")
    snapshot, _ = _capture_file(Path(path), max_bytes, False)
    return snapshot


def verify_file(snapshot: FileSnapshot) -> None:
    if type(snapshot) is not FileSnapshot:
        raise StableFileError("INVALID_FILE_SNAPSHOT")
    _safe_chain(snapshot.path.parent)
    try:
        current = snapshot.path.lstat()
    except OSError as error:
        raise StableFileError("FILE_CHANGED") from error
    _validate_regular(current)
    if (
        _identity(current) != snapshot.identity
        or _file_state(current) != snapshot.state
    ):
        raise StableFileError("FILE_CHANGED")


def snapshot_tree(
    root: Path,
    max_files: int,
    max_bytes: int,
    *,
    excluded: frozenset[str] = frozenset(),
) -> TreeSnapshot:
    if (
        type(max_files) is not int
        or type(max_bytes) is not int
        or max_files < 1
        or max_bytes < 1
    ):
        raise StableFileError("INVALID_TREE_LIMIT")
    absolute = Path(root).absolute()
    _safe_chain(absolute)
    directories: list[DirectorySnapshot] = []
    files: list[tuple[str, FileSnapshot]] = []
    total = 0
    try:
        iterator = os.walk(absolute, topdown=True, followlinks=False)
        for current, names, filenames in iterator:
            parent = Path(current).absolute()
            directories.append(_snapshot_directory(parent))
            names.sort()
            filenames.sort()
            for name in (*names, *filenames):
                if is_reparse(parent / name):
                    raise StableFileError("UNSAFE_REPARSE_PATH")
            for name in filenames:
                path = parent / name
                relative = path.relative_to(absolute).as_posix()
                if relative in excluded:
                    continue
                item = snapshot_file(path, max_bytes)
                total += item.state[0]
                files.append((relative, item))
                if len(files) > max_files or total > max_bytes:
                    raise StableFileError("TREE_TOO_LARGE")
            verify_directory(directories[-1])
    except StableFileError:
        raise
    except OSError as error:
        raise StableFileError("TREE_UNAVAILABLE") from error
    if not directories:
        raise StableFileError("TREE_UNAVAILABLE")
    result = TreeSnapshot(
        absolute,
        tuple(directories),
        tuple(files),
        total,
        max_files,
        max_bytes,
        excluded,
    )
    verify_tree(result)
    return result


def verify_directory(snapshot: DirectorySnapshot) -> None:
    if type(snapshot) is not DirectorySnapshot:
        raise StableFileError("INVALID_DIRECTORY_SNAPSHOT")
    _safe_chain(snapshot.path)
    try:
        current = snapshot.path.lstat()
    except OSError as error:
        raise StableFileError("DIRECTORY_CHANGED") from error
    _validate_directory(current)
    if (
        _identity(current) != snapshot.identity
        or _directory_state(current) != snapshot.state
    ):
        raise StableFileError("DIRECTORY_CHANGED")


def verify_tree(snapshot: TreeSnapshot) -> None:
    if type(snapshot) is not TreeSnapshot:
        raise StableFileError("INVALID_TREE_SNAPSHOT")
    for directory in snapshot.directories:
        verify_directory(directory)
    for _, item in snapshot.files:
        verify_file(item)
    expected_directories = {
        item.path.relative_to(snapshot.root).as_posix()
        for item in snapshot.directories
    }
    expected_files = {relative for relative, _ in snapshot.files}
    actual_directories: set[str] = set()
    actual_files: set[str] = set()
    try:
        for current, names, filenames in os.walk(
            snapshot.root,
            topdown=True,
            followlinks=False,
        ):
            parent = Path(current).absolute()
            actual_directories.add(
                parent.relative_to(snapshot.root).as_posix()
            )
            for name in (*names, *filenames):
                if is_reparse(parent / name):
                    raise StableFileError("UNSAFE_REPARSE_PATH")
            for name in filenames:
                relative = (parent / name).relative_to(
                    snapshot.root
                ).as_posix()
                if relative not in snapshot.excluded:
                    actual_files.add(relative)
    except StableFileError:
        raise
    except OSError as error:
        raise StableFileError("TREE_UNAVAILABLE") from error
    if (
        actual_directories != expected_directories
        or actual_files != expected_files
    ):
        raise StableFileError("TREE_CHANGED")


def _capture_file(
    path: Path,
    max_bytes: int | None,
    retain: bool,
) -> tuple[FileSnapshot, bytes | None]:
    absolute = path.absolute()
    _safe_chain(absolute.parent)
    try:
        before = absolute.lstat()
        _validate_regular(before)
        opened, after_open, digest, content = _read_regular(
            absolute,
            max_bytes,
            retain,
        )
        after_path = absolute.lstat()
    except StableFileError:
        raise
    except OSError as error:
        raise StableFileError("FILE_UNAVAILABLE") from error
    _safe_chain(absolute.parent)
    for value in (opened, after_open, after_path):
        _validate_regular(value)
    path_changed = (
        _identity(after_path) != _identity(before)
        or _file_state(after_path) != _file_state(before)
    )
    handle_changed = (
        _identity(after_open) != _identity(opened)
        or _file_state(after_open) != _file_state(opened)
    )
    cross_api_changed = (
        _identity(opened) != _identity(before)
        or opened.st_size != before.st_size
        or opened.st_mtime_ns != before.st_mtime_ns
    )
    if path_changed or handle_changed or cross_api_changed:
        raise StableFileError("FILE_CHANGED")
    return (
        FileSnapshot(
            absolute,
            _identity(before),
            _file_state(before),
            digest,
        ),
        content,
    )


def _read_regular(
    path: Path,
    max_bytes: int | None,
    retain: bool,
) -> tuple[os.stat_result, os.stat_result, str, bytes | None]:
    flags = os.O_RDONLY | getattr(os, "O_BINARY", 0)
    flags |= getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags)
    with os.fdopen(descriptor, "rb") as stream:
        opened = os.fstat(stream.fileno())
        _validate_regular(opened)
        digest = hashlib.sha256()
        chunks: list[bytes] | None = [] if retain else None
        read = 0
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            read += len(chunk)
            if max_bytes is not None and read > max_bytes:
                raise StableFileError("FILE_TOO_LARGE")
            digest.update(chunk)
            if chunks is not None:
                chunks.append(chunk)
        after = os.fstat(stream.fileno())
    content = b"".join(chunks) if chunks is not None else None
    return opened, after, digest.hexdigest(), content


def _snapshot_directory(path: Path) -> DirectorySnapshot:
    _safe_chain(path)
    try:
        info = path.lstat()
    except OSError as error:
        raise StableFileError("DIRECTORY_UNAVAILABLE") from error
    _validate_directory(info)
    return DirectorySnapshot(
        path,
        _identity(info),
        _directory_state(info),
    )


def _validate_regular(info: os.stat_result) -> None:
    attributes = getattr(info, "st_file_attributes", 0)
    reparse = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    if (
        not stat.S_ISREG(info.st_mode)
        or info.st_nlink != 1
        or not info.st_ino
        or attributes & reparse
    ):
        raise StableFileError("FILE_NOT_SINGLE_LINK")


def _validate_directory(info: os.stat_result) -> None:
    attributes = getattr(info, "st_file_attributes", 0)
    reparse = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    if (
        not stat.S_ISDIR(info.st_mode)
        or not info.st_ino
        or attributes & reparse
    ):
        raise StableFileError("UNSAFE_DIRECTORY")


def _safe_chain(path: Path) -> None:
    for candidate in (path.absolute(), *path.absolute().parents):
        if is_reparse(candidate):
            raise StableFileError("UNSAFE_REPARSE_PATH")


def _identity(info: os.stat_result) -> tuple[int, int]:
    return info.st_dev, info.st_ino


def _file_state(info: os.stat_result) -> tuple[int, int, int]:
    return info.st_size, info.st_mtime_ns, info.st_ctime_ns


def _directory_state(info: os.stat_result) -> tuple[int, int]:
    return info.st_mtime_ns, info.st_ctime_ns
