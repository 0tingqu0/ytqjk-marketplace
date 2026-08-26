from __future__ import annotations

import errno
import os
import stat
import time
from contextlib import contextmanager
from pathlib import Path
from typing import BinaryIO, Iterator


@contextmanager
def exclusive_file_lock(
    path: Path, timeout_seconds: float = 120.0, poll_seconds: float = 0.05
) -> Iterator[None]:
    if timeout_seconds <= 0 or poll_seconds <= 0:
        raise ValueError("文件锁超时和轮询间隔必须大于 0。")
    path.parent.mkdir(parents=True, exist_ok=True)
    _validate_existing(path)
    with path.open("a+b") as handle:
        _validate_open_file(path, handle)
        handle.seek(0)
        _acquire(handle, path, timeout_seconds, poll_seconds)
        try:
            opened = _validate_open_file(path, handle)
            if opened.st_size == 0:
                handle.seek(0)
                handle.write(b"\0")
                handle.flush()
                os.fsync(handle.fileno())
                _validate_open_file(path, handle)
            yield
        finally:
            _release(handle)


def _validate_existing(path: Path) -> None:
    try:
        info = path.lstat()
    except FileNotFoundError:
        return
    except OSError as error:
        raise ValueError("锁文件不可安全读取。") from error
    _validate_info(info)


def _validate_open_file(path: Path, handle: BinaryIO) -> os.stat_result:
    try:
        opened = os.fstat(handle.fileno())
        current = path.lstat()
    except OSError as error:
        raise ValueError("锁文件身份校验失败。") from error
    _validate_info(opened)
    _validate_info(current)
    opened_id = (opened.st_dev, opened.st_ino)
    current_id = (current.st_dev, current.st_ino)
    if opened_id != current_id:
        raise ValueError("锁文件身份校验失败。")
    return opened


def _validate_info(info: os.stat_result) -> None:
    attributes = getattr(info, "st_file_attributes", 0)
    reparse = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    if (
        not stat.S_ISREG(info.st_mode)
        or info.st_nlink != 1
        or attributes & reparse
    ):
        raise ValueError("锁文件必须是安全单链接普通文件。")


def _acquire(handle: BinaryIO, path: Path, timeout: float, poll: float) -> None:
    deadline = time.monotonic() + timeout
    while True:
        try:
            _try_lock(handle)
            return
        except OSError as exc:
            if exc.errno not in {errno.EACCES, errno.EAGAIN, errno.EDEADLK}:
                raise
            if time.monotonic() >= deadline:
                raise TimeoutError(f"等待文件锁超时：{path}") from exc
            time.sleep(poll)


def _try_lock(handle: BinaryIO) -> None:
    if os.name == "nt":
        import msvcrt

        handle.seek(0)
        msvcrt.locking(handle.fileno(), msvcrt.LK_NBLCK, 1)
    else:
        import fcntl

        fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)


def _release(handle: BinaryIO) -> None:
    handle.seek(0)
    if os.name == "nt":
        import msvcrt

        msvcrt.locking(handle.fileno(), msvcrt.LK_UNLCK, 1)
    else:
        import fcntl

        fcntl.flock(handle.fileno(), fcntl.LOCK_UN)
