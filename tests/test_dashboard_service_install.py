from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import dashboard_service_install as service_install


class DashboardServiceInstallTest(unittest.TestCase):
    def test_install_does_not_cut_off_bounded_runtime_steps(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            plugin = root / "plugin"
            script = plugin / "skills/ytqjk/dashboard/dashboard_service.py"
            script.parent.mkdir(parents=True)
            script.touch()
            completed = subprocess.CompletedProcess(
                [],
                0,
                stdout=json.dumps({
                    "status": "RUNNING",
                    "port": 8765,
                    "autostart": "INSTALLED",
                    "changed": True,
                    "document_runtime": {"status": "READY"},
                }),
            )
            with (
                mock.patch.object(
                    service_install,
                    "materialize_dashboard_bundle",
                    return_value=plugin,
                ),
                mock.patch.object(
                    service_install.subprocess,
                    "run",
                    return_value=completed,
                ) as run,
            ):
                receipt = service_install.configure_dashboard(
                    root / "codex", root / "knowledge"
                )

        self.assertEqual(
            run.call_args.kwargs["timeout"],
            service_install.DEFAULT_INSTALL_TIMEOUT,
        )
        command = run.call_args.args[0]
        self.assertNotIn("--document-runtime", command)
        self.assertEqual(receipt["document_runtime"]["status"], "READY")

    def test_install_timeout_can_be_configured(self) -> None:
        with mock.patch.dict(
            os.environ,
            {service_install.INSTALL_TIMEOUT_ENV: "1200"},
            clear=False,
        ):
            self.assertEqual(service_install._install_timeout(None), 1200.0)

    def test_nonzero_preserves_safe_runtime_failure_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            plugin = root / "plugin"
            script = plugin / "skills/ytqjk/dashboard/dashboard_service.py"
            script.parent.mkdir(parents=True)
            script.touch()
            completed = subprocess.CompletedProcess(
                [],
                1,
                stdout=json.dumps({
                    "status": "FAILED",
                    "failure_code": "DOCUMENT_RUNTIME_FAILED",
                    "stderr": "private-token-must-not-leak",
                    "document_runtime": {
                        "status": "FAILED",
                        "runtime_status": "NOT_CONFIGURED",
                        "reason": "GPU_DISTRIBUTION_PRESENT",
                        "stderr": "private-token-must-not-leak",
                    },
                }),
            )
            with (
                mock.patch.object(
                    service_install,
                    "materialize_dashboard_bundle",
                    return_value=plugin,
                ),
                mock.patch.object(
                    service_install.subprocess,
                    "run",
                    return_value=completed,
                ),
            ):
                receipt = service_install.configure_dashboard(
                    root / "codex", root / "knowledge"
                )

        self.assertEqual(
            receipt["failure_code"], "DOCUMENT_RUNTIME_FAILED"
        )
        runtime = receipt["document_runtime"]
        self.assertEqual(runtime["reason"], "GPU_DISTRIBUTION_PRESENT")
        self.assertNotIn("stderr", receipt)
        self.assertNotIn("stderr", runtime)

    def test_timeout_returns_stable_safe_failure_code(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            plugin = root / "plugin"
            script = plugin / "skills/ytqjk/dashboard/dashboard_service.py"
            script.parent.mkdir(parents=True)
            script.touch()
            timeout = subprocess.TimeoutExpired(
                ["dashboard"],
                10,
                stderr="private-token-must-not-leak",
            )
            with (
                mock.patch.object(
                    service_install,
                    "materialize_dashboard_bundle",
                    return_value=plugin,
                ),
                mock.patch.object(
                    service_install.subprocess,
                    "run",
                    side_effect=timeout,
                ),
            ):
                receipt = service_install.configure_dashboard(
                    root / "codex", root / "knowledge"
                )

        self.assertEqual(receipt["status"], "FAILED")
        self.assertEqual(
            receipt["failure_code"], "DASHBOARD_SERVICE_TIMEOUT"
        )
        self.assertNotIn("private-token", json.dumps(receipt))

    def test_legacy_update_allows_request_cleanup_before_restart(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            script = (
                root / "skills/ytqjk/dashboard/dashboard_restart.py"
            )
            script.parent.mkdir(parents=True)
            script.touch()
            with (
                mock.patch.object(
                    service_install,
                    "materialize_dashboard_bundle",
                    return_value=root,
                ),
                mock.patch.object(
                    service_install.subprocess, "Popen"
                ) as popen,
            ):
                service_install.schedule_dashboard_restart(
                    root, root / "knowledge"
                )

        command = popen.call_args.args[0]
        self.assertEqual(command[-2:], ["--delay", "30.0"])


if __name__ == "__main__":
    unittest.main()
