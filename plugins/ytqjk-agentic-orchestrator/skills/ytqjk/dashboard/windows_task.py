"""Windows Task Scheduler integration for the knowledge dashboard."""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path
from typing import Sequence


TASK_NAME = "YTQJK Knowledge Dashboard"
CREATE_NO_WINDOW = getattr(subprocess, "CREATE_NO_WINDOW", 0x08000000)


def _options() -> dict[str, int]:
    return {"creationflags": CREATE_NO_WINDOW} if sys.platform == "win32" else {}


def register(command: Sequence[str]) -> None:
    """Create the current-user logon task and start it immediately."""
    action = subprocess.list2cmdline([str(item) for item in command])
    _run([
        "schtasks.exe",
        "/Create",
        "/TN",
        TASK_NAME,
        "/SC",
        "ONLOGON",
        "/TR",
        action,
        "/RL",
        "LIMITED",
        "/F",
    ], "scheduled task registration failed")
    try:
        _run(
            ["schtasks.exe", "/Run", "/TN", TASK_NAME],
            "scheduled task start failed",
        )
    except RuntimeError:
        try:
            unregister()
        except RuntimeError as rollback_error:
            raise RuntimeError(
                "scheduled task start and rollback failed"
            ) from rollback_error
        raise


def unregister() -> bool:
    """Stop and remove the task if it is registered."""
    if not exists():
        return False
    _run(
        ["schtasks.exe", "/End", "/TN", TASK_NAME],
        "scheduled task stop failed",
        allowed=(0, 1),
    )
    _run(
        ["schtasks.exe", "/Delete", "/TN", TASK_NAME, "/F"],
        "scheduled task removal failed",
    )
    return True


def exists() -> bool:
    completed = subprocess.run(
        ["schtasks.exe", "/Query", "/TN", TASK_NAME],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        check=False,
        **_options(),
    )
    return completed.returncode == 0


def _run(
    command: list[str], message: str, allowed: tuple[int, ...] = (0,)
) -> None:
    completed = subprocess.run(
        command,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        check=False,
        **_options(),
    )
    if completed.returncode not in allowed:
        raise RuntimeError(message)
