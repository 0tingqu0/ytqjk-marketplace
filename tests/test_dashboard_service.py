from __future__ import annotations

import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import unittest
import urllib.request
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SERVICE = (
    ROOT
    / "plugins"
    / "ytqjk-agentic-orchestrator"
    / "skills"
    / "ytqjk"
    / "dashboard"
    / "dashboard_service.py"
)


def free_port() -> int:
    with socket.socket() as current:
        current.bind(("127.0.0.1", 0))
        return int(current.getsockname()[1])


class DashboardServiceTest(unittest.TestCase):
    def test_start_does_not_lock_plugin_directory(self) -> None:
        port = free_port()
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            skill = base / "ytqjk"
            plugin = skill / "dashboard"
            moved_plugin = skill / "dashboard-moved"
            root = base / "knowledge"
            shutil.copytree(SERVICE.parent.parent, skill)
            script = plugin / SERVICE.name
            common = [
                "--knowledge-root",
                str(root),
                "--port",
                str(port),
            ]
            started = subprocess.run(
                [sys.executable, str(script), "start", *common],
                capture_output=True,
                text=True,
                encoding="utf-8",
                check=False,
                timeout=20,
            )
            self.assertEqual(
                started.returncode, 0, started.stderr or started.stdout
            )
            try:
                plugin.rename(moved_plugin)
            finally:
                stop_script = (
                    moved_plugin / SERVICE.name
                    if moved_plugin.exists()
                    else script
                )
                stopped = subprocess.run(
                    [sys.executable, str(stop_script), "stop", *common],
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                    check=False,
                    timeout=20,
                )
                self.assertEqual(
                    stopped.returncode, 0, stopped.stderr or stopped.stdout
                )

    def test_start_survives_launcher_exit_and_stop_cleans_process(self) -> None:
        port = free_port()
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            start = subprocess.run(
                [
                    sys.executable,
                    str(SERVICE),
                    "start",
                    "--knowledge-root",
                    str(root),
                    "--port",
                    str(port),
                ],
                capture_output=True,
                text=True,
                encoding="utf-8",
                check=False,
                timeout=20,
            )
            self.assertEqual(start.returncode, 0, start.stderr or start.stdout)
            receipt = json.loads(start.stdout)
            self.assertEqual(receipt["status"], "RUNNING")

            try:
                with urllib.request.urlopen(
                    f"http://127.0.0.1:{port}/api/snapshot", timeout=3
                ) as response:
                    payload = json.load(response)
                self.assertIn("root", payload)
            finally:
                stop = subprocess.run(
                    [
                        sys.executable,
                        str(SERVICE),
                        "stop",
                        "--knowledge-root",
                        str(root),
                        "--port",
                        str(port),
                    ],
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                    check=False,
                    timeout=20,
                )
                self.assertEqual(stop.returncode, 0, stop.stderr or stop.stdout)

            for _ in range(20):
                try:
                    urllib.request.urlopen(
                        f"http://127.0.0.1:{port}/api/snapshot", timeout=0.2
                    )
                except OSError:
                    break
                time.sleep(0.05)
            else:
                self.fail("dashboard process remained reachable after stop")

    def test_install_registers_autostart_and_uninstall_removes_it(self) -> None:
        port = free_port()
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            root = base / "knowledge"
            startup = base / "startup"
            environment = os.environ.copy()
            environment["YTQJK_DASHBOARD_AUTOSTART_DIR"] = str(startup)
            common = [
                "--knowledge-root",
                str(root),
                "--port",
                str(port),
            ]
            try:
                installed = subprocess.run(
                    [sys.executable, str(SERVICE), "install", *common],
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                    env=environment,
                    check=False,
                    timeout=20,
                )
                self.assertEqual(
                    installed.returncode,
                    0,
                    installed.stderr or installed.stdout,
                )
                receipt = json.loads(installed.stdout)
                self.assertEqual(receipt["autostart"], "INSTALLED")
                self.assertEqual(len(list(startup.iterdir())), 1)
            finally:
                removed = subprocess.run(
                    [sys.executable, str(SERVICE), "uninstall", *common],
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                    env=environment,
                    check=False,
                    timeout=20,
                )
                self.assertEqual(
                    removed.returncode, 0, removed.stderr or removed.stdout
                )
            self.assertEqual(list(startup.iterdir()), [])

    def test_install_restarts_an_existing_service(self) -> None:
        port = free_port()
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            root = base / "knowledge"
            startup = base / "startup"
            environment = os.environ.copy()
            environment["YTQJK_DASHBOARD_AUTOSTART_DIR"] = str(startup)
            common = [
                "--knowledge-root",
                str(root),
                "--port",
                str(port),
            ]
            started = subprocess.run(
                [sys.executable, str(SERVICE), "start", *common],
                capture_output=True,
                text=True,
                encoding="utf-8",
                check=False,
                timeout=20,
            )
            self.assertEqual(
                started.returncode, 0, started.stderr or started.stdout
            )
            state = root / "service" / "dashboard-service.json"
            original_pid = json.loads(state.read_text(encoding="utf-8"))["pid"]
            try:
                installed = subprocess.run(
                    [sys.executable, str(SERVICE), "install", *common],
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                    env=environment,
                    check=False,
                    timeout=20,
                )
                self.assertEqual(
                    installed.returncode,
                    0,
                    installed.stderr or installed.stdout,
                )
                current_pid = json.loads(
                    state.read_text(encoding="utf-8")
                )["pid"]
                self.assertNotEqual(current_pid, original_pid)
            finally:
                subprocess.run(
                    [sys.executable, str(SERVICE), "uninstall", *common],
                    capture_output=True,
                    text=True,
                    encoding="utf-8",
                    env=environment,
                    check=False,
                    timeout=20,
                )


if __name__ == "__main__":
    unittest.main()
