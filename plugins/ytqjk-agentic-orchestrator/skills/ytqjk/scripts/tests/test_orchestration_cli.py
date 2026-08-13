from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).parents[1] / "orchestration_cli.py"
HASH = "a" * 64
SESSION = "c" * 64


class OrchestrationCliTest(unittest.TestCase):
    def test_cli_accepts_hashes_only(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = self.invoke(
                "--database", str(root / "ledger.sqlite"), "--key-file", str(root / "key"),
                "start-run", "--project-id", "project-1", "--objective-hash", HASH,
                "--session-key", SESSION,
            )
        payload = json.loads(result.stdout)
        self.assertEqual(result.returncode, 0)
        self.assertTrue(payload["ok"])
        self.assertNotIn("objective", payload)
        self.assertNotIn("session", payload)

    def test_unknown_or_raw_inputs_never_echo_secret(self) -> None:
        raw = "RAW_SECRET"
        cases = (
            ("--objective-text", raw),
            ("--objective", raw),
            ("start-run", "--unknown", raw),
            ("start-run", raw),
        )
        for arguments in cases:
            with self.subTest(arguments=arguments):
                result = self.invoke(*arguments)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn(raw.encode(), result.stdout)
                self.assertNotIn(raw.encode(), result.stderr)
                self.assertEqual(json.loads(result.stdout)["status"], "invalid identity command")

    def invoke(self, *arguments: str) -> subprocess.CompletedProcess[bytes]:
        return subprocess.run(
            [sys.executable, str(SCRIPT), *arguments], capture_output=True, check=False
        )


if __name__ == "__main__":
    unittest.main()
