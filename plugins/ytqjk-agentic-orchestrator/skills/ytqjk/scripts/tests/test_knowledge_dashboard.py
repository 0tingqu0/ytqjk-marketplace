from __future__ import annotations

import importlib.util
import tempfile
import unittest
import wave
import zipfile
from io import BytesIO
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
DASHBOARD = SCRIPTS.parent / "dashboard" / "knowledge_dashboard.py"
SPEC = importlib.util.spec_from_file_location("knowledge_dashboard", DASHBOARD)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class KnowledgeDashboardTest(unittest.TestCase):
    def office_file(self, parts: dict[str, str]) -> bytes:
        stream = BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            for name, content in parts.items():
                archive.writestr(name, content)
        return stream.getvalue()

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

            self.assertEqual(data["counts"], {"verified": 1, "approved": 1, "candidate": 1, "sessions": 0})
            self.assertEqual({item["state"] for item in data["documents"]}, {"verified", "approved", "candidate"})
            self.assertEqual(data["global_library"]["approved"], 1)

    def test_snapshot_lists_anonymous_session_anchors(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            anchor = root / "sessions" / "hashed" / "anchor.json"
            anchor.parent.mkdir(parents=True)
            anchor.write_text(
                '{"session_key":"a1b2c3d4e5f67890","project_id":"project-a","created_at":"2026-01-01T00:00:00+00:00","last_activity_at":"2026-01-02T00:00:00+00:00","archived_at":null,"memory":"summary"}',
                encoding="utf-8",
            )

            data = MODULE.snapshot(root)

            self.assertEqual(data["counts"]["sessions"], 1)
            self.assertEqual(data["sessions"][0]["key"], "a1b2c3d4e5f6")
            self.assertNotIn("summary", data["sessions"][0].values())

    def test_safe_document_rejects_paths_outside_knowledge_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            document = root / "verified" / "fact.md"
            document.parent.mkdir()
            document.write_text("safe", encoding="utf-8")

            self.assertEqual(MODULE.safe_document(root, "verified/fact.md"), document)
            self.assertIsNone(MODULE.safe_document(root, "../outside.md"))
            self.assertIsNone(MODULE.safe_document(root, "verified/missing.md"))

    def test_intake_creates_candidate_with_analysis(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_document(
                root, "复盘资料.md", "# 部署复盘\n\n确认了候选证据边界。\n"
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
            saved = MODULE.intake_document(root, "guide.md", "部署步骤。", "用于指导发布后的验证")

            self.assertIn("- 作用：用于指导发布后的验证", (root / saved["path"]).read_text(encoding="utf-8"))

    def test_intake_splits_sections_into_traceable_knowledge_chunks(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_document(root, "guide.md", "# 安装\n\n步骤一。\n\n# 验证\n\n步骤二。")
            content = (root / saved["path"]).read_text(encoding="utf-8")
            chunk_root = root / "personal-experience/candidates/imports/chunks"

            self.assertEqual(saved["chunks"], 2)
            self.assertIn("知识片段：2 个", content)
            self.assertEqual(len(list(chunk_root.rglob("*.md"))), 2)
            self.assertIn("source_name: guide.md", next(chunk_root.rglob("*.md")).read_text(encoding="utf-8"))

    def test_intake_assesses_approval_readiness(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            ready = MODULE.intake_document(
                root,
                "evidence.md",
                "来源：https://example.test/evidence\n测试结果：通过。\n" + "可复用结论。" * 30,
            )
            not_ready = MODULE.intake_document(root, "brief.md", "暂存结论")

            self.assertEqual(ready["assessment"]["decision"], "READY_FOR_REVIEW")
            self.assertEqual(ready["state"], "approved")
            self.assertTrue((root / ready["path"]).is_file())
            self.assertEqual(not_ready["assessment"]["decision"], "NOT_READY")
            self.assertIn("## 批准评估", (root / ready["path"]).read_text(encoding="utf-8"))

    def test_not_ready_candidate_can_receive_manual_approval(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_document(root, "brief.md", "待人工确认的资料")
            candidate = root / saved["path"]

            self.assertTrue(candidate.is_file())
            self.assertTrue(MODULE.promote(root, candidate, require_ready=False))
            approved = root / saved["path"].replace("/candidates/", "/approved/")
            self.assertTrue(approved.is_file())
            self.assertIn("approval: manual-dashboard", approved.read_text(encoding="utf-8"))

    def test_intake_rejects_secrets_and_path_traversal(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with self.assertRaises(ValueError):
                MODULE.intake_document(root, "../escape.md", "safe")
            with self.assertRaises(ValueError):
                MODULE.intake_document(root, "token.json", "token: A1B2C3D4E5F6G7")
            with self.assertRaises(ValueError):
                MODULE.intake_document(root, "binary.md", "safe\x00text")

    def test_intake_extracts_modern_office_files(self) -> None:
        files = {
            "brief.docx": self.office_file({"word/document.xml": "<doc><t>Word 结论</t></doc>"}),
            "slides.pptx": self.office_file({"ppt/slides/slide1.xml": "<slide><t>PPT 要点</t></slide>"}),
            "table.xlsx": self.office_file(
                {
                    "xl/sharedStrings.xml": "<sst><si><t>名称</t></si><si><t>数值</t></si></sst>",
                    "xl/worksheets/sheet1.xml": "<sheetData><row><c t='s'><v>0</v></c><c t='s'><v>1</v></c></row></sheetData>",
                }
            ),
        }
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name, source in files.items():
                saved = MODULE.intake_upload(root, name, source)
                content = (root / saved["path"]).read_text(encoding="utf-8")
                self.assertIn("## 入库分析", content)
                self.assertTrue((root / "personal-experience/candidates/imports/originals").is_dir())
                if name == "brief.docx":
                    self.assertIn("Word 结论", content)
                if name == "slides.pptx":
                    self.assertIn("PPT 要点", content)
                if name == "table.xlsx":
                    self.assertIn("名称 | 数值", content)

    def test_intake_records_image_dimensions_and_original(self) -> None:
        png = b"\x89PNG\r\n\x1a\n" + b"\x00\x00\x00\rIHDR" + (320).to_bytes(4, "big") + (200).to_bytes(4, "big")
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_upload(root, "diagram.png", png)
            content = (root / saved["path"]).read_text(encoding="utf-8")

            self.assertIn("图片尺寸：320 x 200", content)
            self.assertIn(f"大小：{len(png)} bytes", content)
            self.assertIn("图片未进行文字识别", content)

    def test_intake_records_wav_audio_metadata_and_folder_path(self) -> None:
        stream = BytesIO()
        with wave.open(stream, "wb") as audio:
            audio.setnchannels(1)
            audio.setsampwidth(2)
            audio.setframerate(8000)
            audio.writeframes(b"\x00\x00" * 8000)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_upload(root, "voice.wav", stream.getvalue(), relative_path="资料/录音/voice.wav")
            content = (root / saved["path"]).read_text(encoding="utf-8")

            self.assertIn("音频信息：WAV，1 声道，8000 Hz，1.0 秒", content)
            self.assertIn("文件夹位置：`资料/录音/voice.wav`", content)

    def test_intake_accepts_source_and_config_text(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            saved = MODULE.intake_document(Path(temporary), "service.toml", "[server]\nport = 8080\n")

            self.assertEqual(saved["state"], "candidate")

    def test_intake_accepts_utf8_bom_and_expanded_text_formats(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_upload(root, "guide.adoc", b"\xef\xbb\xbf= Guide\nUTF-8 content\n")
            makefile = MODULE.intake_upload(root, "Makefile", b"all:\n\techo ready\n")

            self.assertNotIn("\ufeff", (root / saved["path"]).read_text(encoding="utf-8"))
            self.assertEqual(makefile["state"], "candidate")

    def test_intake_accepts_common_legacy_text_encodings(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            saved = MODULE.intake_upload(Path(temporary), "legacy.txt", "\u4e2d\u6587".encode("gb18030"))

            self.assertEqual(saved["state"], "candidate")

    def test_intake_rejects_duplicate_knowledge_with_whitespace_changes(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            first = MODULE.intake_document(root, "first.md", "结论：部署成功。\n证据：测试通过。")

            with self.assertRaisesRegex(ValueError, first["path"]):
                MODULE.intake_document(root, "second.md", "结论：部署成功。\n\n证据：测试通过。")

    def test_intake_allows_same_title_with_different_knowledge(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.intake_document(root, "notes.md", "结论：部署成功。")
            saved = MODULE.intake_document(root, "notes.md", "结论：需要回滚。")

            self.assertEqual(saved["state"], "candidate")

    def test_intake_rejects_unrecognizable_text_upload(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(ValueError, "无法识别文本编码"):
                MODULE.intake_upload(Path(temporary), "binary.txt", b"\x00\x81\xff\x00")

    def test_candidate_update_delete_and_scope(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_document(root, "notes.md", "first version")
            chunks = list((root / "personal-experience/candidates/imports/chunks").glob("*"))
            MODULE.update_candidate(root, saved["path"], "second version")

            self.assertEqual((root / saved["path"]).read_text(encoding="utf-8"), "second version")
            with self.assertRaises(ValueError):
                MODULE.update_candidate(root, "verified/fact.md", "forbidden")
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

            MODULE.update_candidate(root, "error-experience/candidates/test.md", "after")
            MODULE.delete_candidate(root, "error-experience/candidates/test.md")

            self.assertFalse(path.exists())


if __name__ == "__main__":
    unittest.main()
