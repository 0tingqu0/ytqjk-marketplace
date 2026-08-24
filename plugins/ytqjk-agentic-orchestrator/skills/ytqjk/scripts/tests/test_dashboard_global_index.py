from __future__ import annotations

import hashlib
import importlib.util
import json
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path


SKILL_ROOT = Path(__file__).resolve().parents[2]
DASHBOARD_DIR = SKILL_ROOT / "dashboard"
SCRIPTS_DIR = SKILL_ROOT / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))
sys.path.insert(0, str(DASHBOARD_DIR))
SPEC = importlib.util.spec_from_file_location(
    "dashboard_snapshot_global_index", DASHBOARD_DIR / "dashboard_snapshot.py"
)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class DashboardGlobalIndexTest(unittest.TestCase):
    def create_global_index(self, root: Path) -> None:
        cache = root / "global-cache"
        cache.mkdir()
        verified = root / "verified" / "a.md"
        approved = root / "personal-experience" / "approved" / "b.md"
        verified.parent.mkdir()
        approved.parent.mkdir(parents=True)
        verified.write_text("one\ntwo", encoding="utf-8")
        approved.write_text("three", encoding="utf-8")
        verified_hash = hashlib.sha256(verified.read_bytes()).hexdigest()
        approved_hash = hashlib.sha256(approved.read_bytes()).hexdigest()
        (cache / "manifest.json").write_text(
            json.dumps(
                {
                    "indexed_at": "2026-08-24T00:00:00+00:00",
                    "stats": {"files": 2, "chunks": 3},
                }
            ),
            encoding="utf-8",
        )
        connection = sqlite3.connect(cache / "lexical.sqlite3")
        connection.execute(
            "CREATE TABLE chunks "
            "(path, line_start, line_end, content, source_sha256)"
        )
        connection.executemany(
            "INSERT INTO chunks VALUES (?, ?, ?, ?, ?)",
            [
                (
                    "verified/a.md", 1, 1, "one",
                    verified_hash,
                ),
                (
                    "verified/a.md", 2, 2, "two",
                    verified_hash,
                ),
                (
                    "personal-experience/approved/b.md", 1, 1, "three",
                    approved_hash,
                ),
            ],
        )
        connection.commit()
        connection.close()

    def test_global_index_groups_chunks_by_source_file(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.create_global_index(root)

            library = MODULE.global_index_library(root)

            self.assertEqual(library["file_count"], 2)
            self.assertEqual(library["chunk_count"], 3)
            self.assertEqual(library["expected_files"], 2)
            files = {chunks[0]["path"]: chunks for chunks in library["files"]}
            self.assertEqual(files["verified/a.md"][1]["content"], "two")

    def test_global_index_hides_knowledge_after_approval_is_revoked(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.create_global_index(root)
            approved = root / "personal-experience" / "approved" / "b.md"
            candidate = root / "personal-experience" / "candidates" / "b.md"
            candidate.parent.mkdir()
            approved.replace(candidate)

            library = MODULE.global_index_library(root)

            paths = {chunks[0]["path"] for chunks in library["files"]}
            self.assertEqual(paths, {"verified/a.md"})
            self.assertEqual(library["file_count"], 1)
            self.assertEqual(library["chunk_count"], 2)
            self.assertEqual(library["expected_files"], 2)

    def test_global_index_handles_missing_database(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            library = MODULE.global_index_library(Path(temporary))

            self.assertEqual(library["files"], [])
            self.assertEqual(library["file_count"], 0)
            self.assertEqual(library["chunk_count"], 0)


if __name__ == "__main__":
    unittest.main()
