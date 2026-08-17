from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

from codex_guidance import END, START, configure, install, uninstall
from install_external_codex import materialize_plugins


class CodexGuidanceTest(unittest.TestCase):
    def test_install_and_uninstall_preserve_user_guidance(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            codex_root = base / "codex"
            knowledge_root = base / "knowledge"
            codex_root.mkdir()
            agents = codex_root / "AGENTS.md"
            agents.write_text("# User rules\n\n- Keep this.\n", encoding="utf-8")
            materialize_plugins(codex_root)

            first = install(codex_root, knowledge_root)
            repeated = install(codex_root, knowledge_root)

            text = agents.read_text(encoding="utf-8")
            self.assertEqual(first["status"], "INSTALLED")
            self.assertTrue(first["changed"])
            self.assertFalse(repeated["changed"])
            self.assertEqual(text.count(START), 1)
            self.assertEqual(text.count(END), 1)
            self.assertIn("# User rules", text)
            self.assertIn("session_query.py", text)
            self.assertIn("CODEX_THREAD_ID", text)
            self.assertIn("current working directory", text)
            self.assertIn("<project-root>", text)
            self.assertNotIn("<git-project-root>", text)

            removed = uninstall(codex_root)

            self.assertTrue(removed["changed"])
            self.assertEqual(
                agents.read_text(encoding="utf-8"),
                "# User rules\n\n- Keep this.\n",
            )

    def test_nonempty_override_receives_managed_block(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            codex_root = base / "codex"
            codex_root.mkdir()
            override = codex_root / "AGENTS.override.md"
            override.write_text("# Temporary rules\n", encoding="utf-8")
            materialize_plugins(codex_root)

            result = install(codex_root, base / "knowledge")

            self.assertEqual(result["target"], "AGENTS.override.md")
            self.assertIn(START, override.read_text(encoding="utf-8"))
            self.assertFalse((codex_root / "AGENTS.md").exists())

    def test_invalid_markers_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            codex_root = base / "codex"
            codex_root.mkdir()
            (codex_root / "AGENTS.md").write_text(START, encoding="utf-8")
            materialize_plugins(codex_root)

            result = configure(
                codex_root, base / "knowledge", "all", "install"
            )

            self.assertEqual(result["status"], "FAILED")
            self.assertEqual(
                (codex_root / "AGENTS.md").read_text(encoding="utf-8"),
                START,
            )


if __name__ == "__main__":
    unittest.main()
