from __future__ import annotations

import hashlib
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from global_store import is_current_approved_hit, scan_global  # noqa: E402
from rag_common import DEFAULT_CONFIG  # noqa: E402


def approved_row(content: str) -> dict[str, object]:
    return {
        "path": "verified/fact.md",
        "line_start": 1,
        "line_end": 1,
        "content": content,
        "source_sha256": hashlib.sha256(
            content.encode("utf-8")
        ).hexdigest(),
    }


def make_directory_junction(link: Path, target: Path) -> None:
    result = subprocess.run(
        ["cmd.exe", "/d", "/c", "mklink", "/J", str(link), str(target)],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise OSError(result.stderr or result.stdout)


@unittest.skipUnless(os.name == "nt", "Windows junction regression")
class WindowsJunctionSecurityTest(unittest.TestCase):
    def test_scan_global_rejects_approved_root_junction_escape(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            knowledge = base / "knowledge"
            outside = base / "outside"
            knowledge.mkdir()
            outside.mkdir()
            (outside / "fact.md").write_text(
                "OUTSIDE_SCAN_MARKER", encoding="utf-8"
            )
            make_directory_junction(knowledge / "verified", outside)

            chunks, stats = scan_global(knowledge, DEFAULT_CONFIG)

            self.assertEqual(chunks, [])
            self.assertEqual(stats["files"], 0)

    def test_current_hit_rejects_approved_root_junction_escape(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            knowledge = base / "knowledge"
            outside = base / "outside"
            knowledge.mkdir()
            outside.mkdir()
            content = "OUTSIDE_HIT_MARKER"
            (outside / "fact.md").write_text(content, encoding="utf-8")
            make_directory_junction(knowledge / "verified", outside)

            self.assertFalse(
                is_current_approved_hit(knowledge, approved_row(content))
            )


class ApprovedPathSecurityTest(unittest.TestCase):
    def test_current_hit_accepts_regular_file_inside_knowledge_root(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            knowledge = Path(temporary) / "knowledge"
            verified = knowledge / "verified"
            verified.mkdir(parents=True)
            content = "TRUSTED_HIT_MARKER"
            (verified / "fact.md").write_text(content, encoding="utf-8")

            self.assertTrue(
                is_current_approved_hit(knowledge, approved_row(content))
            )

    def test_current_hit_requires_exact_provenance(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            knowledge = Path(temporary) / "knowledge"
            verified = knowledge / "verified"
            verified.mkdir(parents=True)
            content = "first\nsecond"
            (verified / "fact.md").write_text(
                content, encoding="utf-8"
            )
            row = approved_row("first")
            row.pop("source_sha256")
            self.assertFalse(is_current_approved_hit(knowledge, row))
            row = approved_row("first")
            row["source_sha256"] = "0" * 64
            self.assertFalse(is_current_approved_hit(knowledge, row))
            row = approved_row("second")
            self.assertFalse(is_current_approved_hit(knowledge, row))
            row = approved_row("first")
            row["path"] = "verified\\fact.md"
            self.assertFalse(is_current_approved_hit(knowledge, row))

    def test_current_hit_rejects_symlink_component_inside_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            knowledge = Path(temporary) / "knowledge"
            source = knowledge / "verified" / "source"
            source.mkdir(parents=True)
            content = "SYMLINK_HIT_MARKER"
            (source / "fact.md").write_text(content, encoding="utf-8")
            link = knowledge / "verified" / "link"
            try:
                link.symlink_to(source, target_is_directory=True)
            except OSError as error:
                self.skipTest(f"directory symlink unavailable: {error}")
            row = approved_row(content)
            row["path"] = "verified/link/fact.md"

            self.assertFalse(is_current_approved_hit(knowledge, row))


if __name__ == "__main__":
    unittest.main()
