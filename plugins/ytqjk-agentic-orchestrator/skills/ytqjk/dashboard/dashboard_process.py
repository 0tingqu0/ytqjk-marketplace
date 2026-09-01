from __future__ import annotations

import ctypes
import os
import sys
import time


_SYNCHRONIZE = 0x00100000
_WAIT_OBJECT_0 = 0x00000000
_ERROR_INVALID_PARAMETER = 87


def wait_for_process_exit(pid: int, timeout: float) -> bool:
    """Wait until an unrelated service process has fully exited."""
    if pid <= 0:
        return True
    if sys.platform == "win32":
        return _wait_for_windows_process(pid, timeout)
    deadline = time.monotonic() + max(timeout, 0.0)
    while _process_exists(pid) and time.monotonic() < deadline:
        time.sleep(0.05)
    return not _process_exists(pid)


def _wait_for_windows_process(pid: int, timeout: float) -> bool:
    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    open_process = kernel32.OpenProcess
    open_process.argtypes = [ctypes.c_ulong, ctypes.c_int, ctypes.c_ulong]
    open_process.restype = ctypes.c_void_p
    wait = kernel32.WaitForSingleObject
    wait.argtypes = [ctypes.c_void_p, ctypes.c_ulong]
    wait.restype = ctypes.c_ulong
    close = kernel32.CloseHandle
    close.argtypes = [ctypes.c_void_p]
    handle = open_process(_SYNCHRONIZE, False, pid)
    if not handle:
        return ctypes.get_last_error() == _ERROR_INVALID_PARAMETER
    try:
        milliseconds = max(0, round(timeout * 1000))
        return wait(handle, milliseconds) == _WAIT_OBJECT_0
    finally:
        close(handle)


def _process_exists(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except PermissionError:
        return True
    except ProcessLookupError:
        return False
