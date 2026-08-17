"""Run the installer's allowlisted external commands."""
from __future__ import annotations

import os
from pathlib import Path
import shutil
import subprocess
import sys


CREATE_NO_WINDOW = getattr(subprocess, "CREATE_NO_WINDOW", 0x08000000)


def run_external(
    command: list[str], cwd: Path,
    executable_overrides: dict[str, str] | None = None,
    base_environment: dict[str, str] | None = None,
) -> str:
    allowed = {"codex", "npx"}
    if not command or command[0] not in allowed:
        raise RuntimeError("installer rejected external command")
    overrides = executable_overrides or {}
    executable = overrides.get(command[0]) or shutil.which(command[0])
    if executable is None:
        raise RuntimeError(f"required command not found: {command[0]}")
    resolved_command = [executable, *command[1:]]
    environment = base_environment.copy() if base_environment else None
    if command[0] == "npx":
        runtime = cwd / ".ytqjk-npm-runtime"
        home = runtime / "home"
        home.mkdir(parents=True, exist_ok=True)
        environment = environment or os.environ.copy()
        environment.update({
            "HOME": str(home),
            "USERPROFILE": str(home),
            "XDG_CACHE_HOME": str(runtime / "cache"),
            "XDG_CONFIG_HOME": str(runtime / "config"),
            "npm_config_cache": str(runtime / "npm-cache"),
            "npm_config_prefix": str(runtime / "prefix"),
            "npm_config_userconfig": str(runtime / "npmrc"),
        })
    state_check = command[-2:] == ["list", "--json"]
    options: dict[str, object] = {
        "check": True,
        "text": True,
        "shell": False,
        "cwd": cwd,
        "env": environment,
    }
    if state_check:
        options["capture_output"] = True
    else:
        options["stdout"] = sys.stderr
        options["stderr"] = sys.stderr
    if sys.platform == "win32":
        options["creationflags"] = CREATE_NO_WINDOW
    try:
        completed = subprocess.run(resolved_command, **options)
        return completed.stdout or ""
    except subprocess.CalledProcessError as error:
        detail = (error.stderr or error.stdout or "").strip()
        suffix = f": {detail}" if detail else ""
        raise RuntimeError(
            f"external command failed ({error.returncode}){suffix}"
        ) from error
