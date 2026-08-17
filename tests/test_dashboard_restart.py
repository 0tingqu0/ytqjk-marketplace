from __future__ import annotations

import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
MODULE_PATH = (
    ROOT / "plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard"
    / "dashboard_restart.py"
)
SPEC = importlib.util.spec_from_file_location("dashboard_restart", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
RESTART = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RESTART)


class DashboardRestartTest(unittest.TestCase):
    def test_windows_restart_is_delayed_and_hidden(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            with (
                mock.patch.object(RESTART.sys, "platform", "win32"),
                mock.patch.object(RESTART, "interpreter", return_value=Path("pythonw.exe")),
                mock.patch.object(RESTART.subprocess, "Popen") as popen,
            ):
                RESTART.schedule(Path(temporary), 8765, delay=1.5)

        command = popen.call_args.args[0]
        self.assertEqual(command[-2:], ["--delay", "1.5"])
        self.assertTrue(
            popen.call_args.kwargs["creationflags"]
            & getattr(subprocess, "CREATE_NO_WINDOW", 0x08000000)
        )

    def test_scheduler_ignores_duplicate_restart_requests(self) -> None:
        with mock.patch.object(RESTART, "schedule") as schedule:
            callback = RESTART.single_restart_scheduler(Path("knowledge"), 8765)
            callback()
            callback()

        schedule.assert_called_once_with(Path("knowledge"), 8765)


if __name__ == "__main__":
    unittest.main()
