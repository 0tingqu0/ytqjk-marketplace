"""Schedule a dashboard service restart after an HTTP response completes."""
from __future__ import annotations

import argparse
import logging
import os
import subprocess
import sys
import time
from pathlib import Path
from threading import Lock

SCRIPTS_DIR = Path(__file__).resolve().parent.parent / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

from runtime_logging import get_logger, log_event


CREATE_NO_WINDOW = getattr(subprocess, "CREATE_NO_WINDOW", 0x08000000)
LOGGER = get_logger("dashboard.restart")


def interpreter() -> Path:
    current = Path(sys.executable).resolve()
    windowless = current.with_name("pythonw.exe")
    return windowless if sys.platform == "win32" and windowless.is_file() else current


def schedule(root: Path, port: int, delay: float = 1.0) -> None:
    command = [
        str(interpreter()), str(Path(__file__).resolve()),
        "--knowledge-root", str(root.resolve()),
        "--port", str(port), "--delay", str(delay),
    ]
    options: dict[str, object] = {
        "stdin": subprocess.DEVNULL,
        "stdout": subprocess.DEVNULL,
        "stderr": subprocess.DEVNULL,
        "close_fds": True,
    }
    if sys.platform == "win32":
        options["creationflags"] = (
            getattr(subprocess, "DETACHED_PROCESS", 0x00000008)
            | CREATE_NO_WINDOW
        )
    else:
        options["start_new_session"] = True
    subprocess.Popen(command, **options)


def single_restart_scheduler(root: Path, port: int):
    """Return a callback that schedules at most one pending restart."""
    lock = Lock()

    def request() -> None:
        if not lock.acquire(blocking=False):
            return
        try:
            schedule(root, port)
        except OSError:
            log_event(
                LOGGER,
                logging.ERROR,
                "dashboard_restart_schedule_failed",
                port=port,
                reason="PROCESS_START_FAILED",
            )
            lock.release()

    return request


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--knowledge-root", type=Path, required=True)
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--delay", type=float, default=1.0)
    args = parser.parse_args()
    time.sleep(max(0.0, args.delay))
    from dashboard_service import install

    result = install(args.knowledge_root, args.port)
    return 0 if result.get("status") == "RUNNING" else 1


if __name__ == "__main__":
    raise SystemExit(main())
