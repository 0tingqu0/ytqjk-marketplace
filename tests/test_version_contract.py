from __future__ import annotations

import json
import unittest
from pathlib import Path
from unittest import mock

import install_core
from install_core import VERSION


ROOT = Path(__file__).resolve().parents[1]
PLUGIN_NAMES = (
    "ytqjk-agentic-orchestrator",
    "ytqjk-knowledge",
)


class VersionContractTest(unittest.TestCase):
    def test_python_runtime_requires_311(self) -> None:
        with mock.patch.object(install_core.sys, "version_info", (3, 10, 9)):
            with self.assertRaisesRegex(ValueError, "Python 3.11"):
                install_core.require_python()
        with mock.patch.object(install_core.sys, "version_info", (3, 11, 0)):
            install_core.require_python()

    def test_release_version_is_consistent(self) -> None:
        self.assertEqual(VERSION, "0.6.5")
        for plugin_name in PLUGIN_NAMES:
            manifest_path = (
                ROOT
                / "plugins"
                / plugin_name
                / ".codex-plugin"
                / "plugin.json"
            )
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            self.assertEqual(manifest["name"], plugin_name)
            self.assertEqual(manifest["version"], VERSION)

        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        self.assertIn(f"SemVer `{VERSION}`", readme)

    def test_release_documents_runtime_and_boundaries(self) -> None:
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        for marker in (
            "RapidOCR",
            "PP-OCRv6",
            "DocumentFigureClassifier-v2.5",
            "SmolVLM-256M-Instruct",
            "PP-StructureV3",
            "`NOT_CONFIGURED`",
            "Candidates are never automatically approved",
        ):
            self.assertIn(marker, readme)
        notice = (
            ROOT
            / "plugins"
            / "ytqjk-agentic-orchestrator"
            / "THIRD_PARTY_NOTICES.md"
        ).read_text(encoding="utf-8")
        for marker in (
            "Matt Pocock",
            "Docling",
            "RapidOCR",
            "PaddleOCR",
            "SmolVLM-256M-Instruct",
            "DocumentFigureClassifier-v2.5",
        ):
            self.assertIn(marker, notice)


if __name__ == "__main__":
    unittest.main()
