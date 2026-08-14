"""Probe commands resolved from the system PATH before trusting them."""
from __future__ import annotations

from collections.abc import Callable, Mapping
import subprocess


Executor = Callable[
    [list[str], dict[str, str]], subprocess.CompletedProcess[str]
]


def unavailable_commands(
    names: set[str],
    environment: Mapping[str, str],
    which: Callable[[str], str | None],
    executor: Executor,
) -> set[str]:
    """Return commands that are absent or cannot execute a version probe."""
    unavailable: set[str] = set()
    for name in names:
        executable = which(name)
        if executable is None:
            unavailable.add(name)
            continue
        try:
            result = executor(
                [executable, "--version"], dict(environment)
            )
        except (OSError, subprocess.SubprocessError):
            unavailable.add(name)
            continue
        if result.returncode != 0:
            unavailable.add(name)
    return unavailable
