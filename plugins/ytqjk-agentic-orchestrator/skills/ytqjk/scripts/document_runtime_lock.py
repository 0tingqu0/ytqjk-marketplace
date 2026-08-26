"""Crash-safe cross-process lock for document runtime installation."""

from __future__ import annotations

import errno
import os
import stat
import time
from pathlib import Path
from types import TracebackType

from path_safety import is_reparse


_BUSY_ERRNOS = frozenset({errno.EACCES, errno.EAGAIN, errno.EDEADLK})
_BUSY_WINERRORS = frozenset({33, 36})


class RuntimeLockError(RuntimeError):
    """Stable fail-closed lock error."""

    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


class RuntimeInstallLock:
    """Kernel-held lock whose ownership is released when a process exits."""

    def __init__(self, path: Path, timeout_seconds: float) -> None:
        if (
            isinstance(timeout_seconds, bool)
            or not isinstance(timeout_seconds, (int, float))
            or timeout_seconds <= 0
        ):
            raise RuntimeLockError("RUNTIME_INSTALL_LOCK_INVALID")
        self.path = Path(path).absolute()
        self.timeout = float(timeout_seconds)
        self._descriptor: int | None = None

    def __enter__(self) -> RuntimeInstallLock:
        descriptor = self._open_lock_file()
        deadline = time.monotonic() + self.timeout
        try:
            while True:
                try:
                    _lock_descriptor(descriptor)
                    self._descriptor = descriptor
                    return self
                except OSError as error:
                    if not _lock_is_busy(error):
                        raise RuntimeLockError(
                            "RUNTIME_INSTALL_LOCK_FAILED"
                        ) from error
                    if time.monotonic() >= deadline:
                        raise RuntimeLockError(
                            "RUNTIME_INSTALL_LOCK_TIMEOUT"
                        ) from error
                    time.sleep(min(0.05, max(0.0, deadline - time.monotonic())))
        except BaseException:
            os.close(descriptor)
            raise

    def __exit__(
        self,
        exception_type: type[BaseException] | None,
        exception: BaseException | None,
        traceback: TracebackType | None,
    ) -> bool:
        del exception, traceback
        descriptor = self._descriptor
        self._descriptor = None
        if descriptor is None:
            return False
        release_error: OSError | None = None
        try:
            _unlock_descriptor(descriptor)
        except OSError as error:
            release_error = error
        finally:
            os.close(descriptor)
        if release_error is not None and exception_type is None:
            raise RuntimeLockError(
                "RUNTIME_INSTALL_LOCK_RELEASE_FAILED"
            ) from release_error
        return False

    def _open_lock_file(self) -> int:
        flags = os.O_RDWR | os.O_CREAT | getattr(os, "O_BINARY", 0)
        flags |= getattr(os, "O_NOFOLLOW", 0)
        try:
            descriptor = os.open(self.path, flags, 0o600)
            info = os.fstat(descriptor)
            if (
                not stat.S_ISREG(info.st_mode)
                or info.st_nlink != 1
                or is_reparse(self.path)
            ):
                raise RuntimeLockError("RUNTIME_INSTALL_LOCK_UNSAFE")
            if info.st_size == 0:
                os.write(descriptor, b"\0")
                os.fsync(descriptor)
            os.lseek(descriptor, 0, os.SEEK_SET)
            return descriptor
        except RuntimeLockError:
            if "descriptor" in locals():
                os.close(descriptor)
            raise
        except OSError as error:
            if "descriptor" in locals():
                os.close(descriptor)
            raise RuntimeLockError(
                "RUNTIME_INSTALL_LOCK_FAILED"
            ) from error


def _lock_descriptor(descriptor: int) -> None:
    os.lseek(descriptor, 0, os.SEEK_SET)
    if os.name == "nt":
        import msvcrt

        msvcrt.locking(descriptor, msvcrt.LK_NBLCK, 1)
        return
    import fcntl

    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)


def _unlock_descriptor(descriptor: int) -> None:
    os.lseek(descriptor, 0, os.SEEK_SET)
    if os.name == "nt":
        import msvcrt

        msvcrt.locking(descriptor, msvcrt.LK_UNLCK, 1)
        return
    import fcntl

    fcntl.flock(descriptor, fcntl.LOCK_UN)


def _lock_is_busy(error: OSError) -> bool:
    winerror = getattr(error, "winerror", None)
    return error.errno in _BUSY_ERRNOS or winerror in _BUSY_WINERRORS
