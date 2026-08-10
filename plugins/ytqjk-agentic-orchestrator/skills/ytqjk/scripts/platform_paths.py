from __future__ import annotations

import os
import sys
from collections.abc import Mapping
from pathlib import Path, PurePosixPath


KNOWLEDGE_ROOT_ENV = "YTQJK_KNOWLEDGE_ROOT"


def default_knowledge_root(
    *,
    environ: Mapping[str, str] | None = None,
    platform_name: str | None = None,
    home: Path | None = None,
    windows_root: Path | None = None,
) -> Path:
    values = os.environ if environ is None else environ
    configured = values.get(KNOWLEDGE_ROOT_ENV, "").strip()
    if configured:
        return Path(configured).expanduser()

    active_platform = sys.platform if platform_name is None else platform_name
    resolved_home = Path.home() if home is None else home
    if active_platform == "win32":
        drive = Path("D:/") if windows_root is None else windows_root
        if drive.is_dir():
            return drive / "knowledge"
        local = values.get("LOCALAPPDATA", "").strip()
        base = (
            Path(local).expanduser()
            if local
            else resolved_home / "AppData" / "Local"
        )
        return base / "YTQJK" / "knowledge"

    xdg = values.get("XDG_DATA_HOME", "").strip()
    base = (
        Path(xdg)
        if xdg and PurePosixPath(xdg).is_absolute()
        else resolved_home / ".local" / "share"
    )
    return base / "ytqjk"


def runtime_python(runtime_dir: Path, platform_name: str | None = None) -> Path:
    active_platform = sys.platform if platform_name is None else platform_name
    if active_platform == "win32":
        return runtime_dir / "Scripts" / "python.exe"
    return runtime_dir / "bin" / "python"
