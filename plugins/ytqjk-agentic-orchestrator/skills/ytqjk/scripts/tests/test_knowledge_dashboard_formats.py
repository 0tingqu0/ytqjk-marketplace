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


class KnowledgeDashboardFormatsTest(unittest.TestCase):
    def office_file(self, parts: dict[str, str]) -> bytes:
        stream = BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            for name, content in parts.items():
                archive.writestr(name, content)
        return stream.getvalue()

    def test_intake_extracts_modern_office_files(self) -> None:
        files = {
            "brief.docx": self.office_file(
                {"word/document.xml": "<doc><t>Word 结论</t></doc>"}
            ),
            "slides.pptx": self.office_file(
                {"ppt/slides/slide1.xml": "<slide><t>PPT 要点</t></slide>"}
            ),
            "table.xlsx": self.office_file(
                {
                    "xl/sharedStrings.xml": (
                        "<sst><si><t>名称</t></si><si><t>数值</t></si></sst>"
                    ),
                    "xl/worksheets/sheet1.xml": (
                        "<sheetData><row><c t='s'><v>0</v></c>"
                        "<c t='s'><v>1</v></c></row></sheetData>"
                    ),
                }
            ),
        }
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name, source in files.items():
                saved = MODULE.intake_upload(root, name, source)
                content = (root / saved["path"]).read_text(encoding="utf-8")
                self.assertIn("## 入库分析", content)
                originals = (
                    root
                    / "personal-experience/candidates/imports/originals"
                )
                self.assertTrue(originals.is_dir())
                if name == "brief.docx":
                    self.assertIn("Word 结论", content)
                if name == "slides.pptx":
                    self.assertIn("PPT 要点", content)
                if name == "table.xlsx":
                    self.assertIn("名称 | 数值", content)

    def test_intake_records_image_dimensions_and_original(self) -> None:
        png = (
            b"\x89PNG\r\n\x1a\n"
            + b"\x00\x00\x00\rIHDR"
            + (320).to_bytes(4, "big")
            + (200).to_bytes(4, "big")
        )
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_upload(root, "diagram.png", png)
            content = (root / saved["path"]).read_text(encoding="utf-8")

            self.assertIn("图片尺寸：320 x 200", content)
            self.assertIn(f"大小：{len(png)} bytes", content)
            self.assertIn("图片未进行文字识别", content)

    def test_intake_records_wav_metadata_and_folder_path(self) -> None:
        stream = BytesIO()
        with wave.open(stream, "wb") as audio:
            audio.setnchannels(1)
            audio.setsampwidth(2)
            audio.setframerate(8000)
            audio.writeframes(b"\x00\x00" * 8000)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_upload(
                root,
                "voice.wav",
                stream.getvalue(),
                relative_path="资料/录音/voice.wav",
            )
            content = (root / saved["path"]).read_text(encoding="utf-8")

            self.assertIn(
                "音频信息：WAV，1 声道，8000 Hz，1.0 秒",
                content,
            )
            self.assertIn(
                "文件夹位置：`资料/录音/voice.wav`",
                content,
            )

    def test_intake_accepts_source_and_config_text(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            saved = MODULE.intake_document(
                Path(temporary),
                "service.toml",
                "[server]\nport = 8080\n",
            )

            self.assertEqual(saved["state"], "candidate")

    def test_intake_accepts_utf8_bom_and_text_formats(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            saved = MODULE.intake_upload(
                root,
                "guide.adoc",
                b"\xef\xbb\xbf= Guide\nUTF-8 content\n",
            )
            makefile = MODULE.intake_upload(
                root,
                "Makefile",
                b"all:\n\techo ready\n",
            )

            content = (root / saved["path"]).read_text(encoding="utf-8")
            self.assertNotIn("\ufeff", content)
            self.assertEqual(makefile["state"], "candidate")

    def test_intake_accepts_common_legacy_text_encodings(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            saved = MODULE.intake_upload(
                Path(temporary),
                "legacy.txt",
                "中文".encode("gb18030"),
            )

            self.assertEqual(saved["state"], "candidate")

    def test_intake_rejects_unrecognizable_text_upload(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(ValueError, "无法识别文本编码"):
                MODULE.intake_upload(
                    Path(temporary),
                    "binary.txt",
                    b"\x00\x81\xff\x00",
                )


if __name__ == "__main__":
    unittest.main()
