"""Installer adapter for the packaged knowledge dashboard service."""
from __future__ import annotations

import json
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path

from codex_plugin_paths import stable_path


DashboardConfigurator = Callable[
    [Path, Path, str], dict[str, object]
]


def dashboard_receipt(status: str) -> dict[str, object]:
    return {
        "status": status,
        "port": 8765,
        "autostart": "NOT_CONFIGURED",
        "changed": False,
    }


def apply_dashboard_configuration(
    configurator: DashboardConfigurator,
    codex_root: Path,
    knowledge_root: Path,
    mode: str,
    action: str,
) -> dict[str, object]:
    if mode not in ("all", "codex-only"):
        return dashboard_receipt("SKIPPED_MODE")
    try:
        return configurator(codex_root, knowledge_root, action)
    except Exception:
        return dashboard_receipt("FAILED")


def configure_dashboard(
    codex_root: Path,
    knowledge_root: Path,
    action: str = "install",
) -> dict[str, object]:
    if action == "install":
        plugin = stable_path(codex_root, "ytqjk-agentic-orchestrator")
        script = plugin / "skills/ytqjk/dashboard/dashboard_service.py"
    elif action == "uninstall":
        script = (
            Path(__file__).resolve().parent
            / "plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard"
            / "dashboard_service.py"
        )
    else:
        raise ValueError("unsupported dashboard service action")
    if not script.is_file():
        raise RuntimeError("dashboard service entrypoint is missing")
    completed = subprocess.run(
        [
            sys.executable,
            str(script),
            action,
            "--knowledge-root",
            str(knowledge_root.resolve()),
        ],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
        timeout=30,
    )
    if completed.returncode != 0:
        raise RuntimeError("dashboard service configuration failed")
    try:
        value = json.loads(completed.stdout)
    except json.JSONDecodeError as error:
        raise RuntimeError("dashboard service returned invalid receipt") from error
    allowed = {
        key: value[key]
        for key in (
            "status",
            "port",
            "autostart",
            "autostart_kind",
            "autostart_name",
            "changed",
        )
        if key in value
    }
    expected = "RUNNING" if action == "install" else "STOPPED"
    if allowed.get("status") != expected:
        raise RuntimeError("dashboard service did not reach expected state")
    return allowed
