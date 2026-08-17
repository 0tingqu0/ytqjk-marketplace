from __future__ import annotations

import argparse
import json
import os
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer
from pathlib import Path
from threading import Lock, Thread

from knowledge_dashboard import KnowledgeHandler
from platform_paths import default_knowledge_root
from desktop_autostart import install as install_autostart
from desktop_autostart import path as autostart_path
from dashboard_restart import single_restart_scheduler
from windows_task import TASK_NAME, register as register_task
from windows_task import unregister as unregister_task


DASHBOARD_DIR = Path(__file__).resolve().parent
STATE_NAME = "dashboard-service.json"
STOP_NAME = "dashboard-service.stop"
LOG_NAME = "dashboard-service.log"
DEFAULT_PORT = 8765


def service_dir(root: Path) -> Path:
    return root / "service"


def state_path(root: Path) -> Path:
    return service_dir(root) / STATE_NAME


def stop_path(root: Path) -> Path:
    return service_dir(root) / STOP_NAME


def endpoint(port: int) -> str:
    return f"http://127.0.0.1:{port}/api/snapshot"


def healthy(
    port: int, root: Path | None = None, timeout: float = 0.5
) -> bool:
    try:
        with urllib.request.urlopen(endpoint(port), timeout=timeout) as response:
            payload = json.load(response)
        if response.status != 200 or "root" not in payload:
            return False
        return root is None or Path(str(payload["root"])).resolve() == root.resolve()
    except (OSError, ValueError, urllib.error.URLError):
        return False


def atomic_json(path: Path, value: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(f".{os.getpid()}.tmp")
    temporary.write_text(
        json.dumps(value, ensure_ascii=False, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.replace(temporary, path)


def interpreter() -> Path:
    current = Path(sys.executable).resolve()
    if sys.platform == "win32":
        windowless = current.with_name("pythonw.exe")
        if windowless.is_file():
            return windowless
    return current


def service_command(root: Path, port: int) -> list[str]:
    return [
        str(interpreter()),
        str(Path(__file__).resolve()),
        "start",
        "--knowledge-root",
        str(root),
        "--port",
        str(port),
    ]


def run_command(root: Path, port: int) -> list[str]:
    return [
        str(interpreter()),
        str(Path(__file__).resolve()),
        "run",
        "--knowledge-root",
        str(root),
        "--port",
        str(port),
    ]


def scheduled_task_enabled() -> bool:
    return sys.platform == "win32" and not os.environ.get(
        "YTQJK_DASHBOARD_AUTOSTART_DIR", ""
    ).strip()


def wait_for_service(root: Path, port: int) -> bool:
    for _ in range(60):
        if healthy(port, root):
            return True
        time.sleep(0.1)
    return False


def spawn(root: Path, port: int) -> int:
    directory = service_dir(root)
    directory.mkdir(parents=True, exist_ok=True)
    log = (directory / LOG_NAME).open("ab")
    options: dict[str, object] = {
        "stdin": subprocess.DEVNULL,
        "stdout": log,
        "stderr": subprocess.STDOUT,
        "close_fds": True,
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
        process = subprocess.Popen(run_command(root, port), **options)
    finally:
        log.close()
    return process.pid


def start(root: Path, port: int) -> dict[str, object]:
    root = root.resolve()
    if healthy(port, root):
        return {"status": "RUNNING", "port": port, "changed": False}
    if healthy(port):
        return {
            "status": "FAILED",
            "port": port,
            "changed": False,
            "failure_code": "PORT_IN_USE",
        }
    marker = stop_path(root)
    marker.unlink(missing_ok=True)
    pid = spawn(root, port)
    atomic_json(
        state_path(root),
        {"schema": 1, "pid": pid, "port": port, "stop_file": str(marker)},
    )
    if wait_for_service(root, port):
        return {"status": "RUNNING", "port": port, "changed": True}
    return {"status": "FAILED", "port": port, "changed": True}


def stop(root: Path, port: int) -> dict[str, object]:
    root = root.resolve()
    if not healthy(port, root):
        state_path(root).unlink(missing_ok=True)
        stop_path(root).unlink(missing_ok=True)
        return {"status": "STOPPED", "port": port, "changed": False}
    marker = stop_path(root)
    marker.parent.mkdir(parents=True, exist_ok=True)
    marker.touch()
    for _ in range(60):
        if not healthy(port, root):
            for _ in range(60):
                if not state_path(root).exists():
                    marker.unlink(missing_ok=True)
                    return {
                        "status": "STOPPED",
                        "port": port,
                        "changed": True,
                    }
                time.sleep(0.1)
            return {"status": "FAILED", "port": port, "changed": True}
        time.sleep(0.1)
    return {"status": "FAILED", "port": port, "changed": True}


def install(root: Path, port: int) -> dict[str, object]:
    root = root.resolve()
    stopped = stop(root, port)
    if stopped["status"] == "FAILED":
        return {
            "status": "FAILED",
            "port": port,
            "changed": False,
            "autostart": "UNCHANGED",
            "failure_code": "SERVICE_RESTART_FAILED",
        }
    if scheduled_task_enabled():
        autostart_path().unlink(missing_ok=True)
        unregister_task()
        marker = stop_path(root)
        marker.unlink(missing_ok=True)
        try:
            register_task(run_command(root, port))
            running = wait_for_service(root, port)
        except RuntimeError:
            running = False
        if running:
            return {
                "status": "RUNNING",
                "port": port,
                "changed": True,
                "autostart": "INSTALLED",
                "autostart_kind": "scheduled-task",
                "autostart_name": TASK_NAME,
            }
        unregister_task()
    path = install_autostart(service_command(root, port))
    result = start(root, port)
    result["changed"] = bool(stopped["changed"]) or bool(result["changed"])
    if result["status"] == "FAILED":
        path.unlink(missing_ok=True)
        result["autostart"] = "ROLLED_BACK"
    else:
        result["autostart"] = "INSTALLED"
    result["autostart_kind"] = "startup"
    result["autostart_name"] = path.name
    return result


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
    handler = type(
        "RootHandler",
        (KnowledgeHandler,),
        {
            "knowledge_root": root,
            "plugin_root": DASHBOARD_DIR.parents[2],
            "update_lock": Lock(),
            "update_token": secrets.token_urlsafe(32),
            "schedule_restart": staticmethod(
                single_restart_scheduler(root, port)
            ),
        },
    )
    server = ThreadingHTTPServer(("127.0.0.1", port), handler)

    def wait_for_stop() -> None:
        while not marker.exists():
            time.sleep(0.2)
        server.shutdown()

    Thread(target=wait_for_stop, daemon=True).start()
    try:
        server.serve_forever()
    finally:
        server.server_close()


def run_service(root: Path, port: int) -> None:
    """Wait out a same-root legacy server, then own the scheduled task."""
    marker = stop_path(root)
    while healthy(port, root):
        if marker.exists():
            return
        time.sleep(0.2)
    if healthy(port):
        raise RuntimeError("dashboard port is already in use")
    atomic_json(
        state_path(root),
        {
            "schema": 1,
            "pid": os.getpid(),
            "port": port,
            "stop_file": str(marker),
        },
    )
    try:
        serve(root, port)
    finally:
        state_path(root).unlink(missing_ok=True)


def main() -> int:
    parser = argparse.ArgumentParser(description="YTQJK dashboard service.")
    parser.add_argument("command", choices=("install", "start", "run", "stop", "status", "uninstall"))
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    parser.add_argument("--port", type=int, default=DEFAULT_PORT)
    args = parser.parse_args()
    root = args.knowledge_root.resolve()
    if args.command == "run":
        run_service(root, args.port)
        return 0
    if args.command == "install":
        result = install(root, args.port)
    elif args.command == "start":
        result = start(root, args.port)
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
    return 0 if result["status"] not in {"FAILED"} else 1


if __name__ == "__main__":
    raise SystemExit(main())
