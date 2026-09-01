from __future__ import annotations

import subprocess
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DASHBOARD = (
    ROOT
    / "plugins"
    / "ytqjk-agentic-orchestrator"
    / "skills"
    / "ytqjk"
    / "dashboard"
)
sys.path.insert(0, str(DASHBOARD))

from dashboard_process import wait_for_process_exit  # noqa: E402


@unittest.skipUnless(sys.platform == "win32", "Windows process semantics")
class DashboardProcessTest(unittest.TestCase):
    def test_waits_for_process_handle_to_signal(self) -> None:
        process = subprocess.Popen(
            [sys.executable, "-c", "import time; time.sleep(0.3)"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        try:
            self.assertTrue(wait_for_process_exit(process.pid, 2.0))
        finally:
            if process.poll() is None:
                process.terminate()
            process.wait(timeout=2)

    def test_reports_timeout_while_process_is_running(self) -> None:
        process = subprocess.Popen(
            [sys.executable, "-c", "import time; time.sleep(5)"],
            stdin=subprocess.DEVNULL,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        try:
            self.assertFalse(wait_for_process_exit(process.pid, 0.05))
        finally:
            process.terminate()
            process.wait(timeout=2)


if __name__ == "__main__":
    unittest.main()
