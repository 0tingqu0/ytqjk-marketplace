"""Installer adapter for the packaged knowledge dashboard service."""
from __future__ import annotations

import json
import math
import os
import subprocess
import sys
from collections.abc import Callable
from pathlib import Path

from dashboard_bundle import DashboardBundleError
from dashboard_bundle import materialize_dashboard_bundle


CREATE_NO_WINDOW = getattr(subprocess, "CREATE_NO_WINDOW", 0x08000000)
DEFAULT_INSTALL_TIMEOUT: float | None = None
INSTALL_TIMEOUT_ENV = "YTQJK_DASHBOARD_INSTALL_TIMEOUT"
_RECEIPT_FIELDS = (
    "status",
    "port",
    "autostart",
    "autostart_kind",
    "autostart_name",
    "changed",
    "failure_code",
)
_RUNTIME_FIELDS = (
    "schema_version",
    "status",
    "runtime_status",
    "changed",
    "reason",
    "python",
    "requirements_sha256",
    "packages",
    "models",
)


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
    timeout: float | None = None,
) -> dict[str, object]:
    if action == "install":
        try:
            plugin = materialize_dashboard_bundle(codex_root)
        except DashboardBundleError as error:
            return _failed_dashboard_receipt(error.code)
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
    limit = _install_timeout(timeout) if action == "install" else 30.0
    try:
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
            timeout=limit,
        )
    except subprocess.TimeoutExpired:
        return _failed_dashboard_receipt("DASHBOARD_SERVICE_TIMEOUT")
    try:
        allowed = _safe_receipt(completed.stdout)
    except RuntimeError:
        if completed.returncode != 0:
            return _failed_dashboard_receipt("DASHBOARD_SERVICE_FAILED")
        raise
    if completed.returncode != 0:
        if allowed.get("status") != "FAILED":
            return _failed_dashboard_receipt("DASHBOARD_SERVICE_FAILED")
        if not _is_failure_code(allowed.get("failure_code")):
            allowed["failure_code"] = "DASHBOARD_SERVICE_FAILED"
        return allowed
    expected = "RUNNING" if action == "install" else "STOPPED"
    if allowed.get("status") != expected:
        raise RuntimeError("dashboard service did not reach expected state")
    return allowed


def _safe_receipt(output: str) -> dict[str, object]:
    try:
        value = json.loads(output)
    except (TypeError, json.JSONDecodeError) as error:
        message = "dashboard service returned invalid receipt"
        raise RuntimeError(message) from error
    if type(value) is not dict:
        raise RuntimeError("dashboard service returned invalid receipt")
    allowed = {key: value[key] for key in _RECEIPT_FIELDS if key in value}
    if not _is_failure_code(allowed.get("failure_code")):
        allowed.pop("failure_code", None)
    runtime = value.get("document_runtime")
    if type(runtime) is dict:
        allowed["document_runtime"] = {
            key: runtime[key] for key in _RUNTIME_FIELDS if key in runtime
        }
    return allowed


def _is_failure_code(value: object) -> bool:
    return (
        type(value) is str
        and 1 <= len(value) <= 64
        and "A" <= value[0] <= "Z"
        and all(
            "A" <= character <= "Z"
            or "0" <= character <= "9"
            or character == "_"
            for character in value
        )
    )


def _failed_dashboard_receipt(code: str) -> dict[str, object]:
    receipt = dashboard_receipt("FAILED")
    receipt["failure_code"] = code
    return receipt


def _install_timeout(override: float | None) -> float | None:
    value: object = override
    if value is None:
        configured = os.environ.get(INSTALL_TIMEOUT_ENV, "").strip()
        if not configured:
            return DEFAULT_INSTALL_TIMEOUT
        value = configured
    try:
        parsed = float(value)
    except (TypeError, ValueError) as error:
        raise RuntimeError("invalid dashboard install timeout") from error
    if not math.isfinite(parsed) or parsed <= 0:
        raise RuntimeError("invalid dashboard install timeout")
    return parsed


def schedule_dashboard_restart(
    codex_root: Path, knowledge_root: Path
) -> dict[str, object]:
    plugin = materialize_dashboard_bundle(codex_root)
    script = plugin / "skills/ytqjk/dashboard/dashboard_restart.py"
    if not script.is_file():
        raise RuntimeError("dashboard restart entrypoint is missing")
    executable = Path(sys.executable).resolve()
    pythonw = executable.with_name("pythonw.exe")
    if sys.platform == "win32" and pythonw.is_file():
        executable = pythonw
    options: dict[str, object] = {
        "stdin": subprocess.DEVNULL,
        "stdout": subprocess.DEVNULL,
        "stderr": subprocess.DEVNULL,
        "close_fds": True,
        "cwd": str(script.parent),
    }
    if sys.platform == "win32":
        options["creationflags"] = (
            getattr(subprocess, "DETACHED_PROCESS", 0x00000008)
            | CREATE_NO_WINDOW
        )
    else:
        options["start_new_session"] = True
    subprocess.Popen([
        str(executable), str(script),
        "--knowledge-root", str(knowledge_root.resolve()),
        "--port", "8765", "--delay", "30.0",
    ], **options)
    return dashboard_receipt("RESTART_SCHEDULED")
