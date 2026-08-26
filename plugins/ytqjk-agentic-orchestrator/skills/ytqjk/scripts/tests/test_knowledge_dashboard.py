from __future__ import annotations

import hashlib
import importlib.util
import json
import sqlite3
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
            (root / "verified" / "fact.md").write_text(
                "verified",
                encoding="utf-8",
            )
            approved = root / "personal-experience" / "approved"
            approved.mkdir(parents=True)
            (approved / "lesson.md").write_text("approved", encoding="utf-8")
            candidate = root / "error-experience" / "candidates"
            candidate.mkdir(parents=True)
            (candidate / "draft.md").write_text("candidate", encoding="utf-8")

            data = MODULE.snapshot(root)

            self.assertEqual(
                data["counts"],
                {
                    "verified": 1,
                    "approved": 1,
                    "candidate": 1,
                    "sessions": 0,
                },
            )
            self.assertEqual(
                {item["state"] for item in data["documents"]},
                {"verified", "approved", "candidate"},
            )
            self.assertEqual(data["global_library"]["approved"], 1)

    def test_snapshot_lists_anonymous_session_anchors(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            anchor = root / "sessions" / "hashed" / "anchor.json"
            anchor.parent.mkdir(parents=True)
            anchor.write_text(
                json.dumps(
                    {
                        "session_key": "a1b2c3d4e5f67890",
                        "project_id": "project-a",
                        "created_at": "2026-01-01T00:00:00+00:00",
                        "last_activity_at": "2026-01-02T00:00:00+00:00",
                        "archived_at": None,
                        "memory": "summary",
                    }
                ),
                encoding="utf-8",
            )

            data = MODULE.snapshot(root)

            self.assertEqual(data["counts"]["sessions"], 1)
            self.assertEqual(data["sessions"][0]["key"], "a1b2c3d4e5f6")
            self.assertNotIn("summary", data["sessions"][0].values())

    def test_project_library_groups_indexed_chunks_by_file(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            project = root / "projects" / "demo--123"
            project.mkdir(parents=True)
            (project / "manifest.json").write_text(
                '{"identity":{"name":"demo"},'
                '"stats":{"files":1,"chunks":2}}',
                encoding="utf-8",
            )
            connection = sqlite3.connect(project / "lexical.sqlite3")
            connection.execute(
                "CREATE TABLE chunks "
                "(path, line_start, line_end, content)"
            )
            connection.executemany(
                "INSERT INTO chunks VALUES (?, ?, ?, ?)",
                [
                    ("a.py", 1, 2, "one"),
                    ("a.py", 3, 4, "two"),
                ],
            )
            connection.commit()
            connection.close()

            library = MODULE.project_library(root, "demo--123")

            self.assertEqual(library["file_count"], 1)
            self.assertEqual(library["files"][0][1]["content"], "two")
            self.assertIsNone(MODULE.project_library(root, "../outside"))

    def test_project_library_read_does_not_create_empty_cache(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            project = root / "projects" / "demo--123"
            project.mkdir(parents=True)
            (project / "manifest.json").write_text(
                '{"identity":{"name":"demo"},"stats":{}}', encoding="utf-8"
            )

            library = MODULE.project_library(root, "demo--123")

            self.assertEqual(library["prefetch"], [])
            cache = project / "cache" / "global-knowledge.sqlite3"
            self.assertFalse(cache.exists())

    def test_project_library_lists_prefetched_global_knowledge(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            project = root / "projects" / "demo--123"
            project.mkdir(parents=True)
            (project / "manifest.json").write_text(
                '{"identity":{"name":"demo"},"stats":{}}',
                encoding="utf-8",
            )
            source = (
                root / "personal-experience" / "approved" / "session.md"
            )
            source.parent.mkdir(parents=True)
            source.write_text("cached knowledge", encoding="utf-8")
            cache = project / "cache" / "global-knowledge.json"
            cache.parent.mkdir(parents=True)
            cache.write_text(
                json.dumps({"entries": [{
                    "path": "personal-experience/approved/session.md",
                    "line_start": 1,
                    "line_end": 1,
                    "content": "cached knowledge",
                    "source_sha256": hashlib.sha256(
                        source.read_bytes()
                    ).hexdigest(),
                    "query": "cached",
                }]}),
                encoding="utf-8",
            )

            library = MODULE.project_library(root, "demo--123")

            self.assertEqual(
                library["prefetch"][0]["path"],
                "personal-experience/approved/session.md",
            )
            self.assertEqual(library["cache"]["capacity_bytes"], 1024**3)
            self.assertEqual(library["cache"]["policy"], "LFU_LRU")

    def test_project_rows_separate_cache_from_source_index(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            project = root / "projects" / "demo--123"
            project.mkdir(parents=True)
            (project / "manifest.json").write_text(
                '{"identity":{"name":"demo"},"stats":{"files":0,"chunks":0}}',
                encoding="utf-8",
            )
            source = root / "verified" / "fact.md"
            source.parent.mkdir(parents=True)
            source.write_text("cached knowledge", encoding="utf-8")
            cache = project / "cache" / "global-knowledge.json"
            cache.parent.mkdir(parents=True)
            cache.write_text(
                json.dumps({"entries": [{
                    "path": "verified/fact.md",
                    "line_start": 1,
                    "line_end": 1,
                    "content": "cached knowledge",
                    "source_sha256": hashlib.sha256(
                        source.read_bytes()
                    ).hexdigest(),
                    "query": "cached",
                }]}),
                encoding="utf-8",
            )

            project_row = MODULE.build_snapshot(
                root,
                MODULE.safe_document,
            )["projects"][0]

            self.assertEqual(project_row["cache"]["entries"], 1)
            self.assertEqual(project_row["files"], 0)
            self.assertEqual(project_row["chunks"], 0)

    def test_safe_document_rejects_paths_outside_knowledge_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            document = root / "verified" / "fact.md"
            document.parent.mkdir()
            document.write_text("safe", encoding="utf-8")

            self.assertEqual(
                MODULE.safe_document(root, "verified/fact.md"),
                document,
            )
            self.assertIsNone(MODULE.safe_document(root, "../outside.md"))
            self.assertIsNone(MODULE.safe_document(root, "verified/missing.md"))


if __name__ == "__main__":
    unittest.main()
