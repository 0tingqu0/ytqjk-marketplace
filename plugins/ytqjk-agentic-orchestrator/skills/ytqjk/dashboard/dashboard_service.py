from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Callable

from dashboard_process import wait_for_process_exit
from dashboard_restart import single_restart_scheduler
from dashboard_server_runtime import serve_dashboard
from desktop_autostart import install as install_autostart
from desktop_autostart import path as autostart_path
import document_runtime_service as runtime_service
from platform_paths import default_knowledge_root
from windows_task import TASK_NAME, register as register_task
from windows_task import unregister as unregister_task


STATE_NAME, STOP_NAME = "dashboard-service.json", "dashboard-service.stop"
LOG_NAME, DEFAULT_PORT = "dashboard-service.log", 8765
RuntimePreparer = Callable[[Path, str], dict[str, object]]
CONFIGURE_RUNTIME = runtime_service.configured_document_runtime


def state_path(root: Path) -> Path:
    return root / "service" / STATE_NAME


def stop_path(root: Path) -> Path:
    return root / "service" / STOP_NAME


def healthy(
    port: int, root: Path | None = None, timeout: float = 0.5
) -> bool:
    try:
        endpoint = f"http://127.0.0.1:{port}/api/snapshot"
        request = urllib.request.urlopen(endpoint, timeout=timeout)
        with request as response:
            payload = json.load(response)
        if response.status != 200 or "root" not in payload:
            return False
        actual = Path(str(payload["root"])).resolve()
        return root is None or actual == root.resolve()
    except (OSError, ValueError, urllib.error.URLError):
        return False


def atomic_json(path: Path, value: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(f".{os.getpid()}.tmp")
    content = json.dumps(value, ensure_ascii=False, sort_keys=True)
    temporary.write_text(content + "\n", encoding="utf-8")
    os.replace(temporary, path)


def service_command(
    root: Path, port: int, executable: Path | None = None,
    document_runtime: str = "auto",
) -> list[str]:
    return runtime_service.dashboard_command(
        Path(__file__), root, port, "start", executable, document_runtime
    )


def run_command(
    root: Path, port: int, executable: Path | None = None
) -> list[str]:
    return runtime_service.dashboard_command(
        Path(__file__), root, port, "run", executable
    )


def scheduled_task_enabled() -> bool:
    configured = os.environ.get("YTQJK_DASHBOARD_AUTOSTART_DIR", "")
    return sys.platform == "win32" and not configured.strip()


def wait_for_service(root: Path, port: int) -> bool:
    for _ in range(60):
        if healthy(port, root):
            return True
        time.sleep(0.1)
    return False


def spawn(root: Path, port: int, executable: Path | None = None) -> int:
    directory = root / "service"
    directory.mkdir(parents=True, exist_ok=True)
    log = (directory / LOG_NAME).open("ab")
    options: dict[str, object] = {
        "stdin": subprocess.DEVNULL, "stdout": log,
        "stderr": subprocess.STDOUT, "close_fds": True,
        "cwd": str(directory),
    }
    if sys.platform == "win32":
        options["creationflags"] = (
            subprocess.CREATE_NEW_PROCESS_GROUP
            | subprocess.DETACHED_PROCESS
            | subprocess.CREATE_NO_WINDOW
        )
    else:
        options["start_new_session"] = True
    try:
        command = run_command(root, port, executable)
        process = subprocess.Popen(command, **options)
    finally:
        log.close()
    return process.pid


def start(
    root: Path, port: int, executable: Path | None = None
) -> dict[str, object]:
    root = root.resolve()
    if healthy(port, root):
        return _result("RUNNING", port, False)
    if healthy(port):
        return _failed(port, "PORT_IN_USE")
    marker = stop_path(root)
    marker.unlink(missing_ok=True)
    pid = spawn(root, port, executable)
    state = {"schema": 1, "pid": pid, "port": port}
    state["stop_file"] = str(marker)
    atomic_json(state_path(root), state)
    if wait_for_service(root, port):
        return _result("RUNNING", port, True)
    return _result("FAILED", port, True)


def start_configured(
    root: Path, port: int, mode: str = "auto",
    preparer: RuntimePreparer | None = None,
) -> dict[str, object]:
    executable, receipt = CONFIGURE_RUNTIME(root, mode, preparer)
    if mode != "off" and executable is None:
        return _runtime_failure(port, receipt)
    result = start(root, port, executable)
    result["document_runtime"] = receipt
    return result


def stop(root: Path, port: int) -> dict[str, object]:
    root = root.resolve()
    pid = _service_pid(root)
    if not healthy(port, root):
        state_path(root).unlink(missing_ok=True)
        stop_path(root).unlink(missing_ok=True)
        if pid is not None and not wait_for_process_exit(pid, 6.0):
            return _result("FAILED", port, True)
        return _result("STOPPED", port, False)
    marker = stop_path(root)
    marker.parent.mkdir(parents=True, exist_ok=True)
    marker.touch()
    for _ in range(60):
        if not healthy(port, root):
            for _ in range(60):
                if not state_path(root).exists():
                    marker.unlink(missing_ok=True)
                    if pid is None or wait_for_process_exit(pid, 6.0):
                        return _result("STOPPED", port, True)
                    return _result("FAILED", port, True)
                time.sleep(0.1)
            return _result("FAILED", port, True)
        time.sleep(0.1)
    return _result("FAILED", port, True)


def _service_pid(root: Path) -> int | None:
    try:
        value = json.loads(state_path(root).read_text(encoding="utf-8"))["pid"]
    except (KeyError, OSError, TypeError, ValueError):
        return None
    return value if isinstance(value, int) and value > 0 else None


def install(
    root: Path, port: int, document_runtime: str = "off",
    preparer: RuntimePreparer | None = None,
) -> dict[str, object]:
    root = root.resolve()
    executable, receipt = CONFIGURE_RUNTIME(
        root, document_runtime, preparer
    )
    if document_runtime != "off" and executable is None:
        return _runtime_failure(port, receipt, "UNCHANGED")
    stopped = stop(root, port)
    if stopped["status"] == "FAILED":
        result = _failed(port, "SERVICE_RESTART_FAILED")
        result["autostart"] = "UNCHANGED"
        result["document_runtime"] = receipt
        return result
    if scheduled_task_enabled():
        scheduled = _install_task(root, port, executable)
        if scheduled is not None:
            scheduled["document_runtime"] = receipt
            return scheduled
    command = service_command(root, port, executable, document_runtime)
    path = install_autostart(command)
    result = start(root, port, executable)
    result["changed"] = bool(stopped["changed"]) or bool(result["changed"])
    result["autostart"] = "INSTALLED"
    if result["status"] == "FAILED":
        path.unlink(missing_ok=True)
        result["autostart"] = "ROLLED_BACK"
    result["autostart_kind"] = "startup"
    result["autostart_name"] = path.name
    result["document_runtime"] = receipt
    return result


def _install_task(
    root: Path, port: int, executable: Path | None
) -> dict[str, object] | None:
    autostart_path().unlink(missing_ok=True)
    unregister_task()
    stop_path(root).unlink(missing_ok=True)
    try:
        register_task(run_command(root, port, executable))
        running = wait_for_service(root, port)
    except RuntimeError:
        running = False
    if not running:
        unregister_task()
        return None
    return _result(
        "RUNNING", port, True,
        autostart="INSTALLED",
        autostart_kind="scheduled-task",
        autostart_name=TASK_NAME,
    )


def uninstall(root: Path, port: int) -> dict[str, object]:
    if scheduled_task_enabled():
        legacy = autostart_path()
        legacy_removed = legacy.exists()
        legacy.unlink(missing_ok=True)
        result = stop(root, port)
        removed = unregister_task()
        result["autostart"] = "REMOVED"
        result["autostart_kind"] = "scheduled-task"
        result["autostart_name"] = TASK_NAME
        result["changed"] = (
            legacy_removed or removed or bool(result["changed"])
        )
        return result
    path = autostart_path()
    changed = path.exists()
    path.unlink(missing_ok=True)
    result = stop(root, port)
    result["autostart"] = "REMOVED"
    result["changed"] = changed or bool(result["changed"])
    return result


def serve(root: Path, port: int) -> None:
    marker = stop_path(root)
    serve_dashboard(
        root,
        port,
        marker,
        single_restart_scheduler(root, port),
    )


def run_service(root: Path, port: int) -> None:
    marker = stop_path(root)
    while healthy(port, root):
        if marker.exists():
            return
        time.sleep(0.2)
    if healthy(port):
        raise RuntimeError("dashboard port is already in use")
    state = {"schema": 1, "pid": os.getpid(), "port": port}
    state["stop_file"] = str(marker)
    atomic_json(state_path(root), state)
    try:
        serve(root, port)
    finally:
        state_path(root).unlink(missing_ok=True)


def _runtime_failure(
    port: int, receipt: dict[str, object], autostart: str | None = None
) -> dict[str, object]:
    result = _failed(port, "DOCUMENT_RUNTIME_FAILED")
    result["document_runtime"] = receipt
    if autostart is not None:
        result["autostart"] = autostart
    return result


def _failed(port: int, code: str) -> dict[str, object]:
    return _result("FAILED", port, False, failure_code=code)


def _result(
    status: str, port: int, changed: bool, **extra: object
) -> dict[str, object]:
    return {"status": status, "port": port, "changed": changed, **extra}


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="YTQJK dashboard service.")
    commands = ("install", "start", "run", "stop", "status", "uninstall")
    parser.add_argument("command", choices=commands)
    parser.add_argument(
        "--knowledge-root", type=Path, default=default_knowledge_root()
    )
    parser.add_argument("--port", type=int, default=DEFAULT_PORT)
    parser.add_argument(
        "--document-runtime", choices=("auto", "off"), default="auto"
    )
    return parser


def main() -> int:
    args = _parser().parse_args()
    root = args.knowledge_root.resolve()
    if args.command == "run":
        run_service(root, args.port)
        return 0
    if args.command == "install":
        result = install(root, args.port, args.document_runtime)
    elif args.command == "start":
        result = start_configured(root, args.port, args.document_runtime)
    elif args.command == "stop":
        result = stop(root, args.port)
    elif args.command == "uninstall":
        result = uninstall(root, args.port)
    else:
        result = {
            "status": "RUNNING" if healthy(args.port, root) else "STOPPED",
            "port": args.port,
        }
    print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    return 0 if result["status"] != "FAILED" else 1


if __name__ == "__main__":
    raise SystemExit(main())
