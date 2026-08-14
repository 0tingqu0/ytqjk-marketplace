from __future__ import annotations

import codecs
import os
from pathlib import Path
import shutil
import subprocess
import unittest


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


if __name__ == "__main__":
    unittest.main()
