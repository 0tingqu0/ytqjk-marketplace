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

    def test_external_runner_resolves_windows_command_shim(self) -> None:
        executable = r"C:\Program Files\nodejs\npx.CMD"
        completed = mock.Mock(stdout="")
        with tempfile.TemporaryDirectory() as directory:
            with (
                mock.patch("setup.shutil.which", return_value=executable),
                mock.patch("setup.subprocess.run", return_value=completed) as run,
            ):
                run_external(["npx", "--version"], Path(directory))

        self.assertEqual(run.call_args.args[0], [executable, "--version"])

    def test_external_runner_reports_missing_command(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            with (
                mock.patch("setup.shutil.which", return_value=None),
                mock.patch("setup.subprocess.run") as run,
                self.assertRaisesRegex(RuntimeError, "command not found: npx"),
            ):
                run_external(["npx", "--version"], Path(directory))

        run.assert_not_called()

    def test_apply_reports_missing_npx_before_staging(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = io.StringIO()
            available = lambda name: None if name == "npx" else name
            with (
                mock.patch("setup.shutil.which", side_effect=available),
                redirect_stderr(output),
            ):
                code = main(
                    [
                        "--apply",
                        "--yes",
                        "--json",
                        "--mode",
                        "ide-only",
                        "--target-root",
                        directory,
                    ],
                )

        receipt = json.loads(output.getvalue())
        self.assertEqual(code, 2)
        self.assertEqual(receipt["error"], "required command not found: npx")


if __name__ == "__main__":
    unittest.main()
