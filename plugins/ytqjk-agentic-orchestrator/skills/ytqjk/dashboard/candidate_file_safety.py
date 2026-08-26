"""Single-link snapshots and atomic writes for candidate artifacts."""

from __future__ import annotations

import hashlib
import os
import stat
import uuid
from contextlib import ExitStack, contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator

from file_lock import exclusive_file_lock
from path_safety import is_reparse


class CandidateFileError(ValueError):
    pass


@dataclass(frozen=True)
class FileSnapshot:
    root: Path
    path: Path
    content: bytes
    sha256: str
    identity: tuple[int, int]
    state: tuple[int, int, int]


def read_file_snapshot(
    root: Path,
    path: Path,
    max_bytes: int,
) -> FileSnapshot:
    root = root.absolute()
    path = path.absolute()
    if max_bytes <= 0 or not _within(path, root):
        raise CandidateFileError("UNSAFE_CANDIDATE_ARTIFACT")
    _safe_chain(root, path)
    try:
        before = path.lstat()
        _validate_regular(before)
        with path.open("rb") as handle:
            opened = os.fstat(handle.fileno())
            _validate_regular(opened)
            _require_same(before, opened)
            content = handle.read(max_bytes + 1)
            after_open = os.fstat(handle.fileno())
        after_path = path.lstat()
    except CandidateFileError:
        raise
    except OSError as error:
        raise CandidateFileError("CANDIDATE_FILE_UNAVAILABLE") from error
    _validate_regular(after_open)
    _validate_regular(after_path)
    _require_stable(before, opened, after_open, after_path)
    if len(content) != before.st_size or len(content) > max_bytes:
        raise CandidateFileError("CANDIDATE_FILE_CHANGED")
    return FileSnapshot(
        root,
        path,
        content,
        hashlib.sha256(content).hexdigest(),
        (before.st_dev, before.st_ino),
        _state(before),
    )


def verify_snapshot(snapshot: FileSnapshot) -> None:
    current = read_file_snapshot(
        snapshot.root,
        snapshot.path,
        max(len(snapshot.content), 1),
    )
    if (
        current.identity != snapshot.identity
        or current.state != snapshot.state
        or current.sha256 != snapshot.sha256
        or current.content != snapshot.content
    ):
        raise CandidateFileError("CANDIDATE_FILE_CHANGED")


def verify_snapshots(snapshots: tuple[FileSnapshot, ...]) -> None:
    for snapshot in snapshots:
        verify_snapshot(snapshot)


def atomic_replace_file(
    root: Path,
    path: Path,
    content: bytes,
    expected: FileSnapshot | None = None,
) -> FileSnapshot:
    root = root.absolute()
    path = path.absolute()
    if not _within(path, root):
        raise CandidateFileError("UNSAFE_CANDIDATE_ARTIFACT")
    _safe_chain(root, path.parent)
    if expected is not None:
        verify_snapshot(expected)
        if expected.path != path:
            raise CandidateFileError("CANDIDATE_FILE_CHANGED")
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    flags |= getattr(os, "O_BINARY", 0)
    descriptor = -1
    try:
        descriptor = os.open(temporary, flags, 0o600)
        with os.fdopen(descriptor, "wb") as handle:
            descriptor = -1
            _validate_regular(os.fstat(handle.fileno()))
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
            written = os.fstat(handle.fileno())
            _validate_regular(written)
            if written.st_size != len(content):
                raise CandidateFileError("ATOMIC_WRITE_INCOMPLETE")
        temporary_snapshot = read_file_snapshot(
            root, temporary, max(len(content), 1),
        )
        if (
            temporary_snapshot.identity
            != (written.st_dev, written.st_ino)
            or temporary_snapshot.content != content
        ):
            raise CandidateFileError("ATOMIC_WRITE_INCOMPLETE")
        _safe_chain(root, path.parent)
        if expected is not None:
            verify_snapshot(expected)
        verify_snapshot(temporary_snapshot)
        os.replace(temporary, path)
        result = read_file_snapshot(root, path, max(len(content), 1))
        if result.content != content:
            raise CandidateFileError("ATOMIC_WRITE_INCOMPLETE")
        return result
    except CandidateFileError:
        raise
    except OSError as error:
        raise CandidateFileError("ATOMIC_WRITE_FAILED") from error
    finally:
        if descriptor >= 0:
            os.close(descriptor)
        temporary.unlink(missing_ok=True)


def unlink_snapshot(snapshot: FileSnapshot) -> None:
    verify_snapshot(snapshot)
    try:
        snapshot.path.unlink()
    except OSError as error:
        raise CandidateFileError("CANDIDATE_DELETE_FAILED") from error


@contextmanager
def candidate_lifecycle_lock(
    root: Path,
    document: Path | str,
) -> Iterator[Path]:
    raw_root = root.absolute()
    value = Path(document)
    raw_candidate = (
        value if value.is_absolute() else raw_root / value
    ).absolute()
    raw_allowed = (
        raw_root / "personal-experience" / "candidates",
        raw_root / "error-experience" / "candidates",
    )
    if not any(_within(raw_candidate, item) for item in raw_allowed):
        raise CandidateFileError("CANDIDATE_LOCK_SCOPE_INVALID")
    _safe_chain(raw_root, raw_candidate)
    try:
        canonical_root = raw_root.resolve(strict=True)
        candidate = raw_candidate.resolve(strict=False)
    except (OSError, RuntimeError) as error:
        raise CandidateFileError("CANDIDATE_LOCK_PATH_INVALID") from error
    allowed = (
        canonical_root / "personal-experience" / "candidates",
        canonical_root / "error-experience" / "candidates",
    )
    if candidate.suffix.casefold() != ".md" or not any(
        _within(candidate, item) for item in allowed
    ):
        raise CandidateFileError("CANDIDATE_LOCK_SCOPE_INVALID")
    key = hashlib.sha256(
        os.path.normcase(str(candidate)).encode("utf-8")
    ).hexdigest()
    locks = canonical_root / ".locks"
    try:
        locks.mkdir(exist_ok=True)
        if not locks.is_dir():
            raise CandidateFileError("CANDIDATE_LOCK_PATH_INVALID")
        _safe_chain(canonical_root, locks)
    except OSError as error:
        raise CandidateFileError("CANDIDATE_LOCK_UNAVAILABLE") from error
    lock = locks / f"candidate-{key}.lock"
    stack = ExitStack()
    try:
        stack.enter_context(exclusive_file_lock(lock))
        _safe_chain(canonical_root, lock)
    except Exception:
        stack.close()
        raise
    with stack:
        yield candidate


def _validate_regular(info: os.stat_result) -> None:
    attributes = getattr(info, "st_file_attributes", 0)
    reparse = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    if (
        not stat.S_ISREG(info.st_mode)
        or info.st_nlink != 1
        or not info.st_ino
        or attributes & reparse
    ):
        raise CandidateFileError("CANDIDATE_FILE_NOT_SINGLE_LINK")


def _require_same(first: os.stat_result, second: os.stat_result) -> None:
    if (
        first.st_dev,
        first.st_ino,
        first.st_size,
    ) != (
        second.st_dev,
        second.st_ino,
        second.st_size,
    ):
        raise CandidateFileError("CANDIDATE_FILE_CHANGED")


def _require_stable(*values: os.stat_result) -> None:
    before, opened, after_open, after_path = values
    for value in values[1:]:
        _require_same(before, value)
    path_times = (before.st_mtime_ns, before.st_ctime_ns)
    final_path_times = (after_path.st_mtime_ns, after_path.st_ctime_ns)
    handle_times = (opened.st_mtime_ns, opened.st_ctime_ns)
    final_handle_times = (after_open.st_mtime_ns, after_open.st_ctime_ns)
    if path_times != final_path_times or handle_times != final_handle_times:
        raise CandidateFileError("CANDIDATE_FILE_CHANGED")


def _identity(info: os.stat_result) -> tuple[int, int]:
    return info.st_dev, info.st_ino


def _state(info: os.stat_result) -> tuple[int, int, int]:
    return info.st_size, info.st_mtime_ns, info.st_ctime_ns


def _safe_chain(root: Path, path: Path) -> None:
    current = path.absolute()
    if not _within(current, root):
        raise CandidateFileError("UNSAFE_CANDIDATE_ARTIFACT")
    while True:
        if current.exists() and is_reparse(current):
            raise CandidateFileError("UNSAFE_REPARSE_PATH")
        if current == root:
            return
        current = current.parent


def _within(path: Path, parent: Path) -> bool:
    try:
        path.absolute().relative_to(parent.absolute())
    except ValueError:
        return False
    return True
