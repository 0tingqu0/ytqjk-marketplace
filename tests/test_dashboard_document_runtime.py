from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


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
SPEC = importlib.util.spec_from_file_location(
    "dashboard_service_runtime_test", DASHBOARD / "dashboard_service.py"
)
assert SPEC is not None and SPEC.loader is not None
SERVICE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SERVICE)


def _ready(python: Path) -> dict[str, object]:
    return {
        "status": "READY",
        "runtime_status": "READY",
        "reason": None,
        "python": str(python),
    }


def _failed() -> dict[str, object]:
    return {
        "status": "FAILED",
        "runtime_status": "NOT_CONFIGURED",
        "reason": "COMMAND_FAILED",
        "python": None,
    }


class DashboardDocumentRuntimeTest(unittest.TestCase):
    def test_cli_defaults_to_auto(self) -> None:
        arguments = SERVICE._parser().parse_args(["install"])
        self.assertEqual(arguments.document_runtime, "auto")

    def test_default_auto_calls_preparer_and_switches_python(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            python = root / ".runtime/document-intake/venv/bin/python"
            python.parent.mkdir(parents=True)
            python.touch()
            calls: list[tuple[Path, str]] = []

            def prepare(path: Path, mode: str) -> dict[str, object]:
                calls.append((path, mode))
                return _ready(python)

            with mock.patch.object(
                SERVICE, "start", return_value={
                    "status": "RUNNING", "port": 8765, "changed": True,
                }
            ) as start:
                result = SERVICE.start_configured(
                    root, 8765, preparer=prepare
                )

        self.assertEqual(calls, [(root, "auto")])
        start.assert_called_once_with(root, 8765, python)
        self.assertEqual(result["document_runtime"]["status"], "READY")

    def test_failed_auto_does_not_stop_or_start_service(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            with (
                mock.patch.object(SERVICE, "stop") as stop,
                mock.patch.object(SERVICE, "start") as start,
                mock.patch.object(SERVICE, "install_autostart") as autostart,
            ):
                result = SERVICE.install(
                    root, 8765, "auto", lambda path, mode: _failed()
                )

        self.assertEqual(result["status"], "FAILED")
        self.assertEqual(result["failure_code"], "DOCUMENT_RUNTIME_FAILED")
        stop.assert_not_called()
        start.assert_not_called()
        autostart.assert_not_called()

    def test_ready_install_uses_runtime_for_service_commands(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            python = root / ".runtime/document-intake/venv/bin/python"
            python.parent.mkdir(parents=True)
            python.touch()
            startup = Path(temporary) / "startup.desktop"
            with (
                mock.patch.object(
                    SERVICE,
                    "stop",
                    return_value={"status": "STOPPED", "changed": False},
                ),
                mock.patch.object(
                    SERVICE, "scheduled_task_enabled", return_value=False
                ),
                mock.patch.object(
                    SERVICE, "install_autostart", return_value=startup
                ) as autostart,
                mock.patch.object(
                    SERVICE,
                    "start",
                    return_value={
                        "status": "RUNNING", "port": 8765,
                        "changed": True,
                    },
                ) as start,
            ):
                result = SERVICE.install(
                    root,
                    8765,
                    "auto",
                    lambda path, mode: _ready(python),
                )

        start.assert_called_once_with(root.resolve(), 8765, python)
        command = autostart.call_args.args[0]
        self.assertEqual(Path(command[0]), python.resolve())
        self.assertEqual(command[-2:], ["--document-runtime", "auto"])
        self.assertEqual(result["document_runtime"]["status"], "READY")

    def test_off_reports_not_configured_without_fake_ready(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            with mock.patch.object(
                SERVICE,
                "start",
                return_value={
                    "status": "RUNNING", "port": 8765, "changed": True,
                },
            ):
                result = SERVICE.start_configured(root, 8765, "off")

        receipt = result["document_runtime"]
        self.assertEqual(receipt["status"], "SKIPPED")
        self.assertEqual(receipt["runtime_status"], "NOT_CONFIGURED")
        self.assertIsNone(receipt["python"])


if __name__ == "__main__":
    unittest.main()
