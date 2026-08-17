from __future__ import annotations

import json
import unittest
from pathlib import Path

from install_core import VERSION


ROOT = Path(__file__).resolve().parents[1]
PLUGIN_NAMES = (
    "ytqjk-agentic-orchestrator",
    "ytqjk-knowledge",
)


class VersionContractTest(unittest.TestCase):
    def test_release_version_is_consistent(self) -> None:
        self.assertEqual(VERSION, "0.5.0")
        for plugin_name in PLUGIN_NAMES:
            manifest_path = (
                ROOT / "plugins" / plugin_name / ".codex-plugin" / "plugin.json"
            )
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            self.assertEqual(manifest["name"], plugin_name)
            self.assertEqual(manifest["version"], VERSION)

        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        self.assertIn(f"SemVer `{VERSION}`", readme)


if __name__ == "__main__":
    unittest.main()
