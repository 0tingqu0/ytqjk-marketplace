"""Startup-file integration for non-scheduled dashboard launches."""
from __future__ import annotations

import html
import os
import shlex
import subprocess
import sys
from pathlib import Path
from typing import Sequence


def install(command: Sequence[str]) -> Path:
    configured = os.environ.get("YTQJK_DASHBOARD_AUTOSTART_DIR", "").strip()
    override = Path(configured).expanduser().resolve() if configured else None
    encoding = "utf-8"
    if sys.platform == "win32":
        appdata = Path(os.environ.get("APPDATA", Path.home() / "AppData/Roaming"))
        target = override or appdata / "Microsoft/Windows/Start Menu/Programs/Startup"
        path = target / "YTQJK Knowledge Dashboard.vbs"
        line = subprocess.list2cmdline([_validated_line(item) for item in command])
        escaped = line.replace('"', '""')
        content = (
            'Set shell = CreateObject("WScript.Shell")\r\n'
            f'shell.Run "{escaped}", 0, False\r\n'
        )
        encoding = "utf-16"
    elif sys.platform == "darwin":
        target = override or Path.home() / "Library/LaunchAgents"
        path = target / "com.yitingqujiukun.ytqjk-knowledge.plist"
        arguments = "".join(
            f"      <string>{html.escape(item)}</string>\n" for item in command
        )
        content = (
            '<?xml version="1.0" encoding="UTF-8"?>\n'
            '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" '
            '"http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n'
            '<plist version="1.0"><dict>\n'
            '  <key>Label</key><string>com.yitingqujiukun.ytqjk-knowledge</string>\n'
            f"  <key>ProgramArguments</key><array>\n{arguments}  </array>\n"
            '  <key>RunAtLoad</key><true/>\n'
            '</dict></plist>\n'
        )
    else:
        target = override or Path.home() / ".config/autostart"
        path = target / "ytqjk-knowledge.desktop"
        line = shlex.join(command)
        content = (
            "[Desktop Entry]\nType=Application\nName=YTQJK Knowledge Dashboard\n"
            f"Exec={line}\nTerminal=false\nX-GNOME-Autostart-enabled=true\n"
        )
    target.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding=encoding)
    return path


def path() -> Path:
    configured = os.environ.get("YTQJK_DASHBOARD_AUTOSTART_DIR", "").strip()
    override = Path(configured).expanduser().resolve() if configured else None
    if sys.platform == "win32":
        appdata = Path(os.environ.get("APPDATA", Path.home() / "AppData/Roaming"))
        target = override or appdata / "Microsoft/Windows/Start Menu/Programs/Startup"
        return target / "YTQJK Knowledge Dashboard.vbs"
    if sys.platform == "darwin":
        target = override or Path.home() / "Library/LaunchAgents"
        return target / "com.yitingqujiukun.ytqjk-knowledge.plist"
    target = override or Path.home() / ".config/autostart"
    return target / "ytqjk-knowledge.desktop"


def _validated_line(value: str) -> str:
    if any(character in value for character in ('\r', '\n', '"')):
        raise ValueError("service path contains unsupported characters")
    return value
