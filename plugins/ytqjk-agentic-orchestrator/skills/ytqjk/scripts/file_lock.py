from __future__ import annotations

import errno
import os
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
    with path.open("a+b") as handle:
        if handle.tell() == 0:
            handle.write(b"\0")
            handle.flush()
        handle.seek(0)
        _acquire(handle, path, timeout_seconds, poll_seconds)
        try:
            yield
        finally:
            _release(handle)


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
