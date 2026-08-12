from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
DASHBOARD = SCRIPTS.parent / "dashboard" / "knowledge_dashboard.py"
SPEC = importlib.util.spec_from_file_location("knowledge_dashboard", DASHBOARD)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class KnowledgeDashboardTest(unittest.TestCase):
    def test_snapshot_separates_approved_and_candidates(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "verified").mkdir()
            (root / "verified" / "fact.md").write_text("verified", encoding="utf-8")
            approved = root / "personal-experience" / "approved"
            approved.mkdir(parents=True)
            (approved / "lesson.md").write_text("approved", encoding="utf-8")
            candidate = root / "error-experience" / "candidates"
            candidate.mkdir(parents=True)
            (candidate / "draft.md").write_text("candidate", encoding="utf-8")

            data = MODULE.snapshot(root)

            self.assertEqual(data["counts"], {"verified": 1, "approved": 1, "candidate": 1})
            self.assertEqual({item["state"] for item in data["documents"]}, {"verified", "approved", "candidate"})

    def test_safe_document_rejects_paths_outside_knowledge_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            document = root / "verified" / "fact.md"
            document.parent.mkdir()
            document.write_text("safe", encoding="utf-8")

            self.assertEqual(MODULE.safe_document(root, "verified/fact.md"), document)
            self.assertIsNone(MODULE.safe_document(root, "../outside.md"))
            self.assertIsNone(MODULE.safe_document(root, "verified/missing.md"))


if __name__ == "__main__":
    unittest.main()
