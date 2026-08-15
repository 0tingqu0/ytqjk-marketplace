from __future__ import annotations

import codecs
from contextlib import redirect_stderr
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

from bootstrap_cli_runtime import CliRuntime
from setup import main, run_external


ROOT = Path(__file__).resolve().parents[1]
LAUNCHER = ROOT / "install.ps1"


class WindowsLauncherTest(unittest.TestCase):
    def test_launcher_has_utf8_bom_for_windows_powershell(self) -> None:
        self.assertTrue(LAUNCHER.read_bytes().startswith(codecs.BOM_UTF8))

    def test_launcher_parses_with_windows_powershell(self) -> None:
        executable = shutil.which("powershell.exe")
        if executable is None:
            self.skipTest("Windows PowerShell is unavailable")
        command = (
            "$tokens = $null; $errors = $null; "
            "[System.Management.Automation.Language.Parser]::ParseFile("
            "$env:YTQJK_TEST_SCRIPT, [ref]$tokens, [ref]$errors) | Out-Null; "
            "if ($errors.Count) { $errors | ForEach-Object Message; exit 1 }"
        )
        environment = os.environ.copy()
        environment["YTQJK_TEST_SCRIPT"] = str(LAUNCHER)

        completed = subprocess.run(
            [
                executable,
                "-NoProfile",
                "-NonInteractive",
                "-Command",
                command,
            ],
            cwd=ROOT,
            env=environment,
            capture_output=True,
            check=False,
        )

        output = (completed.stdout + completed.stderr).decode(errors="replace")
        self.assertEqual(completed.returncode, 0, output)

    def test_launcher_bootstraps_python_after_executable_probe(self) -> None:
        launcher = LAUNCHER.read_text(encoding="utf-8-sig")

        self.assertIn("Test-PythonRuntime", launcher)
        self.assertIn("Python.Python.3.12", launcher)
        self.assertIn("--scope user", launcher)
        self.assertIn("--disable-interactivity", launcher)

    def test_external_runner_resolves_windows_command_shim(self) -> None:
        executable = r"C:\Program Files\nodejs\npx.CMD"
        completed = mock.Mock(stdout="")
        with tempfile.TemporaryDirectory() as directory:
            with (
                mock.patch(
                    "external_command_runner.shutil.which",
                    return_value=executable,
                ),
                mock.patch(
                    "external_command_runner.subprocess.run",
                    return_value=completed,
                ) as run,
            ):
                run_external(["npx", "--version"], Path(directory))

        self.assertEqual(run.call_args.args[0], [executable, "--version"])

    def test_external_runner_reports_missing_command(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with (
                mock.patch(
                    "external_command_runner.shutil.which", return_value=None
                ),
                mock.patch("external_command_runner.subprocess.run") as run,
                self.assertRaisesRegex(RuntimeError, "command not found: npx"),
            ):
                run_external(["npx", "--version"], Path(directory))

        run.assert_not_called()

    def test_apply_bootstraps_missing_npx_and_codex(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = io.StringIO()
            runtime = CliRuntime(
                status="BOOTSTRAPPED",
                root=Path(directory) / "runtime",
                executables={"codex": "codex.cmd", "npx": "npx.cmd"},
                environment=os.environ.copy(),
                provisioned=("node", "codex"),
            )
            codex_root = Path(directory) / "codex"
            with (
                mock.patch(
                    "setup.ensure_cli_runtime", return_value=runtime
                ) as bootstrap,
                mock.patch(
                    "setup.apply_plan",
                    return_value={"status": "APPLIED"},
                ),
                mock.patch(
                    "setup.configure_dashboard",
                    return_value={
                        "status": "RUNNING",
                        "port": 8765,
                        "autostart": "INSTALLED",
                        "changed": True,
                    },
                ) as dashboard,
                redirect_stderr(output),
            ):
                code = main(
                    [
                        "--apply",
                        "--yes",
                        "--json",
                        "--mode", "all",
                        "--target-root",
                        directory,
                        "--codex-root", str(codex_root),
                        "--codex-import", "off",
                        "--project-bootstrap", "off",
                    ],
                )

            self.assertEqual(code, 0, output.getvalue())
            bootstrap.assert_called_once_with({"codex", "npx"})
            self.assertTrue(codex_root.is_dir())
            dashboard.assert_called_once()


if __name__ == "__main__":
    unittest.main()
