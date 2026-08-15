from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
MODULE_PATH = (
    ROOT
    / "plugins"
    / "ytqjk-agentic-orchestrator"
    / "skills"
    / "ytqjk"
    / "dashboard"
    / "windows_task.py"
)
SPEC = importlib.util.spec_from_file_location("dashboard_windows_task", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
WINDOWS_TASK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(WINDOWS_TASK)


class WindowsTaskTest(unittest.TestCase):
    def test_register_uses_long_running_run_action(self) -> None:
        completed = mock.Mock(returncode=0)
        command = [
            r"C:\Python\pythonw.exe",
            r"C:\plugin\dashboard_service.py",
            "run",
            "--knowledge-root",
            r"D:\knowledge",
            "--port",
            "8765",
        ]
        with mock.patch.object(
            WINDOWS_TASK.subprocess, "run", return_value=completed
        ) as run:
            WINDOWS_TASK.register(command)

        create = run.call_args_list[0].args[0]
        trigger = run.call_args_list[1].args[0]
        self.assertEqual(
            create[:4],
            ["schtasks.exe", "/Create", "/TN", WINDOWS_TASK.TASK_NAME],
        )
        self.assertIn(
            "run --knowledge-root D:\\knowledge --port 8765", create[7]
        )
        self.assertEqual(
            trigger,
            ["schtasks.exe", "/Run", "/TN", WINDOWS_TASK.TASK_NAME],
        )

    def test_unregister_is_idempotent_when_task_is_absent(self) -> None:
        missing = mock.Mock(returncode=1)
        with mock.patch.object(
            WINDOWS_TASK.subprocess, "run", return_value=missing
        ) as run:
            changed = WINDOWS_TASK.unregister()

        self.assertFalse(changed)
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0][:2], ["schtasks.exe", "/Query"])

    def test_register_rolls_back_when_immediate_start_fails(self) -> None:
        results = [
            mock.Mock(returncode=0),
            mock.Mock(returncode=1),
            mock.Mock(returncode=0),
            mock.Mock(returncode=0),
            mock.Mock(returncode=0),
        ]
        with (
            mock.patch.object(
                WINDOWS_TASK.subprocess, "run", side_effect=results
            ) as run,
            self.assertRaisesRegex(RuntimeError, "scheduled task start failed"),
        ):
            WINDOWS_TASK.register(["pythonw.exe", "service.py", "run"])

        commands = [call.args[0][1] for call in run.call_args_list]
        self.assertEqual(
            commands, ["/Create", "/Run", "/Query", "/End", "/Delete"]
        )


if __name__ == "__main__":
    unittest.main()
