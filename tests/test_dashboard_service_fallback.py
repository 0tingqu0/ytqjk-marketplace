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
    "dashboard_service_fallback", DASHBOARD / "dashboard_service.py"
)
assert SPEC is not None and SPEC.loader is not None
SERVICE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(SERVICE)


class DashboardServiceFallbackTest(unittest.TestCase):
    def test_scheduled_task_failure_uses_startup_file(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            startup = Path(temporary) / "YTQJK Dashboard.vbs"
            with (
                mock.patch.object(
                    SERVICE,
                    "stop",
                    return_value={"status": "STOPPED", "changed": False},
                ),
                mock.patch.object(
                    SERVICE, "scheduled_task_enabled", return_value=True
                ),
                mock.patch.object(SERVICE, "autostart_path") as legacy,
                mock.patch.object(SERVICE, "unregister_task"),
                mock.patch.object(
                    SERVICE,
                    "register_task",
                    side_effect=RuntimeError("access denied"),
                ),
                mock.patch.object(
                    SERVICE, "install_autostart", return_value=startup
                ) as fallback,
                mock.patch.object(
                    SERVICE,
                    "start",
                    return_value={
                        "status": "RUNNING",
                        "port": 8765,
                        "changed": True,
                    },
                ),
            ):
                result = SERVICE.install(root, 8765)

        legacy.return_value.unlink.assert_called_once_with(missing_ok=True)
        fallback.assert_called_once()
        self.assertEqual(result["status"], "RUNNING")
        self.assertEqual(result["autostart"], "INSTALLED")
        self.assertEqual(result["autostart_kind"], "startup")


if __name__ == "__main__":
    unittest.main()
