from __future__ import annotations

import argparse
import html
import json
import os
import secrets
import shlex
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
    for _ in range(60):
        if healthy(port, root):
            return {"status": "RUNNING", "port": port, "changed": True}
        time.sleep(0.1)
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
            state_path(root).unlink(missing_ok=True)
            marker.unlink(missing_ok=True)
            return {"status": "STOPPED", "port": port, "changed": True}
        time.sleep(0.1)
    return {"status": "FAILED", "port": port, "changed": True}


def _validated_line(value: str) -> str:
    if any(character in value for character in ('\r', '\n', '"')):
        raise ValueError("service path contains unsupported characters")
    return value


def install_autostart(root: Path, port: int) -> Path:
    command = service_command(root, port)
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


def autostart_path() -> Path:
    configured = os.environ.get("YTQJK_DASHBOARD_AUTOSTART_DIR", "").strip()
    override = Path(configured).expanduser().resolve() if configured else None
    if sys.platform == "win32":
        appdata = Path(os.environ.get("APPDATA", Path.home() / "AppData/Roaming"))
        target = override or appdata / (
            "Microsoft/Windows/Start Menu/Programs/Startup/"
        )
        return target / "YTQJK Knowledge Dashboard.vbs"
    if sys.platform == "darwin":
        target = override or Path.home() / "Library/LaunchAgents"
        return target / "com.yitingqujiukun.ytqjk-knowledge.plist"
    target = override or Path.home() / ".config/autostart"
    return target / "ytqjk-knowledge.desktop"


def install(root: Path, port: int) -> dict[str, object]:
    path = install_autostart(root.resolve(), port)
    result = start(root, port)
    if result["status"] == "FAILED":
        path.unlink(missing_ok=True)
        result["autostart"] = "ROLLED_BACK"
    else:
        result["autostart"] = "INSTALLED"
    result["autostart_kind"] = "startup"
    result["autostart_name"] = path.name
    return result


def uninstall(root: Path, port: int) -> dict[str, object]:
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


def main() -> int:
    parser = argparse.ArgumentParser(description="YTQJK dashboard service.")
    parser.add_argument("command", choices=("install", "start", "run", "stop", "status", "uninstall"))
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    parser.add_argument("--port", type=int, default=DEFAULT_PORT)
    args = parser.parse_args()
    root = args.knowledge_root.resolve()
    if args.command == "run":
        serve(root, args.port)
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
