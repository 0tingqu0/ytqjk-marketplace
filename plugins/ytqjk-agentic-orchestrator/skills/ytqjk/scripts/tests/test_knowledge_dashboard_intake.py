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


class KnowledgeDashboardIntakeTest(unittest.TestCase):
    def test_intake_creates_candidate_with_analysis(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_document(
                root,
                "复盘资料.md",
                "# 部署复盘\n\n确认了候选证据边界。\n",
            )
            document = root / saved["path"]
            content = document.read_text(encoding="utf-8")

            self.assertEqual(saved["state"], "candidate")
            self.assertFalse(saved["path"].endswith(".md.md"))
            self.assertIn("status: CANDIDATE", content)
            self.assertIn("## 入库分析", content)
            self.assertIn("## 原始资料", content)

    def test_intake_records_optional_purpose(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_document(
                root,
                "guide.md",
                "部署步骤。",
                "用于指导发布后的验证",
            )
            content = (root / saved["path"]).read_text(encoding="utf-8")

            self.assertIn("- 作用：用于指导发布后的验证", content)

    def test_intake_splits_sections_into_traceable_chunks(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_document(
                root,
                "guide.md",
                "# 安装\n\n步骤一。\n\n# 验证\n\n步骤二。",
            )
            content = (root / saved["path"]).read_text(encoding="utf-8")
            chunk_root = (
                root / "personal-experience/candidates/imports/chunks"
            )

            self.assertEqual(saved["chunks"], 2)
            self.assertIn("知识片段：2 个", content)
            self.assertEqual(len(list(chunk_root.rglob("*.md"))), 2)
            first = next(chunk_root.rglob("*.md"))
            self.assertIn(
                "source_name: guide.md",
                first.read_text(encoding="utf-8"),
            )

    def test_intake_assesses_approval_readiness(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            ready = MODULE.intake_document(
                root,
                "evidence.md",
                "来源：https://example.test/evidence\n测试结果：通过。\n"
                + "可复用结论。" * 30,
            )
            not_ready = MODULE.intake_document(
                root,
                "brief.md",
                "暂存结论",
            )

            self.assertEqual(
                ready["assessment"]["decision"],
                "READY_FOR_REVIEW",
            )
            self.assertEqual(ready["state"], "candidate")
            self.assertIn("/candidates/", ready["path"])
            self.assertTrue((root / ready["path"]).is_file())
            self.assertEqual(
                not_ready["assessment"]["decision"],
                "NOT_READY",
            )
            content = (root / ready["path"]).read_text(encoding="utf-8")
            self.assertIn("## 批准评估", content)

    def test_not_ready_candidate_can_receive_manual_approval(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_document(
                root,
                "brief.md",
                "待人工确认的资料",
            )
            candidate = root / saved["path"]

            self.assertTrue(candidate.is_file())
            self.assertTrue(
                MODULE.promote(root, candidate, require_ready=False)
            )
            approved = root / saved["path"].replace(
                "/candidates/",
                "/approved/",
            )
            self.assertTrue(approved.is_file())
            content = approved.read_text(encoding="utf-8")
            self.assertIn("approval: manual-dashboard", content)

    def test_ready_candidate_stays_candidate_after_edit(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            ready_content = (
                "来源：https://example.test/evidence\n测试结果：通过。\n"
                + "可复用结论。" * 30
            )
            saved = MODULE.intake_document(
                root,
                "evidence.md",
                ready_content,
            )

            updated = MODULE.update_candidate(
                root,
                saved["path"],
                ready_content + "\n补充证据。",
            )
            MODULE.snapshot(root)

            self.assertEqual(updated["state"], "candidate")
            self.assertTrue((root / saved["path"]).is_file())
            approved = root / saved["path"].replace(
                "/candidates/",
                "/approved/",
            )
            self.assertFalse(approved.exists())

    def test_intake_rejects_secrets_and_path_traversal(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaises(ValueError):
                MODULE.intake_document(root, "../escape.md", "safe")
            with self.assertRaises(ValueError):
                MODULE.intake_document(
                    root,
                    "token.json",
                    "token: A1B2C3D4E5F6G7",
                )
            with self.assertRaises(ValueError):
                MODULE.intake_document(root, "binary.md", "safe\x00text")

    def test_intake_rejects_duplicate_knowledge(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            first = MODULE.intake_document(
                root,
                "first.md",
                "结论：部署成功。\n证据：测试通过。",
            )

            with self.assertRaisesRegex(ValueError, first["path"]):
                MODULE.intake_document(
                    root,
                    "second.md",
                    "结论：部署成功。\n\n证据：测试通过。",
                )

    def test_intake_allows_same_title_with_different_knowledge(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.intake_document(root, "notes.md", "结论：部署成功。")
            saved = MODULE.intake_document(
                root,
                "notes.md",
                "结论：需要回滚。",
            )

            self.assertEqual(saved["state"], "candidate")

    def test_candidate_update_delete_and_scope(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_document(
                root,
                "notes.md",
                "first version",
            )
            chunk_root = (
                root / "personal-experience/candidates/imports/chunks"
            )
            chunks = list(chunk_root.glob("*"))
            MODULE.update_candidate(root, saved["path"], "second version")

            content = (root / saved["path"]).read_text(encoding="utf-8")
            self.assertEqual(content, "second version")
            with self.assertRaises(ValueError):
                MODULE.update_candidate(
                    root,
                    "verified/fact.md",
                    "forbidden",
                )
            MODULE.delete_candidate(root, saved["path"])
            self.assertFalse((root / saved["path"]).exists())
            self.assertTrue(chunks)
            self.assertFalse(chunks[0].exists())

    def test_candidate_actions_allow_error_candidate(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "error-experience/candidates/test.md"
            path.parent.mkdir(parents=True)
            path.write_text("before", encoding="utf-8")

            MODULE.update_candidate(
                root,
                "error-experience/candidates/test.md",
                "after",
            )
            MODULE.delete_candidate(
                root,
                "error-experience/candidates/test.md",
            )

            self.assertFalse(path.exists())


if __name__ == "__main__":
    unittest.main()
