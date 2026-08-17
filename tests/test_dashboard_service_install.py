from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
from unittest import mock

import dashboard_service_install as service_install


class DashboardServiceInstallTest(unittest.TestCase):
    def test_legacy_update_allows_request_cleanup_before_restart(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            script = (
                root / "plugins/ytqjk-agentic-orchestrator/skills/ytqjk"
                / "dashboard/dashboard_restart.py"
            )
            script.parent.mkdir(parents=True)
            script.touch()
            with mock.patch.object(
                service_install.subprocess, "Popen"
            ) as popen:
                service_install.schedule_dashboard_restart(
                    root, root / "knowledge"
                )

        command = popen.call_args.args[0]
        self.assertEqual(command[-2:], ["--delay", "30.0"])


if __name__ == "__main__":
    unittest.main()
