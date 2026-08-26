from __future__ import annotations

import sqlite3
import sys
import tempfile
import unittest
from contextlib import closing
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import global_session_query  # noqa: E402
from project_prefetch import prefetch_stats, update_prefetch  # noqa: E402
from project_tracking import identify_project  # noqa: E402
from rag_test_support import make_repo, run_rag  # noqa: E402


class RagHotPathTest(unittest.TestCase):
    def test_global_fallback_does_not_rescan_approved_corpus(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"
            approved = knowledge / "verified"
            approved.mkdir(parents=True)
            (approved / "fact.md").write_text(
                "QUERY_HOT_PATH_MARKER_731", encoding="utf-8"
            )
            run_rag(knowledge, "index-global", None, "--vector-mode", "off")

            with mock.patch.object(
                global_session_query,
                "scan_global",
                side_effect=AssertionError(
                    "query path rescanned global knowledge"
                ),
                create=True,
            ):
                result = global_session_query.query_global(
                    knowledge, repo, "QUERY_HOT_PATH_MARKER_731",
                    "no-rescan-session", identify_project(repo)["id"], 5,
                )

            self.assertEqual(result["status"], "GLOBAL_FALLBACK_HIT")

    def test_project_cache_hit_avoids_capacity_tree_scan(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"
            project_id = identify_project(repo)["id"]
            project_dir = knowledge / "projects" / project_id
            source = knowledge / "verified" / "fact.md"
            source.parent.mkdir(parents=True)
            source.write_text("FAST_PROJECT_CACHE_MARKER_912", encoding="utf-8")
            update_prefetch(
                project_dir, "FAST_PROJECT_CACHE_MARKER_912",
                [{"path": "verified/fact.md", "line_start": 1, "line_end": 1,
                  "content": "FAST_PROJECT_CACHE_MARKER_912"}],
                generation="generation-one",
            )

            with mock.patch(
                "project_prefetch._directory_size",
                side_effect=AssertionError("query path walked project cache"),
            ), mock.patch(
                "project_prefetch.enforce_project_capacity",
                side_effect=AssertionError(
                    "query path enforced write-time capacity"
                ),
            ):
                result = global_session_query.query_global(
                    knowledge, repo, "FAST_PROJECT_CACHE_MARKER_912",
                    "fast-cache-session", project_id, 5,
                )

            self.assertEqual(result["status"], "PROJECT_CACHE_HIT")

    def test_prefetch_stats_uses_unbounded_sql_aggregate(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            knowledge = Path(temporary) / "knowledge"
            project_dir = knowledge / "projects" / "project"
            source = knowledge / "verified" / "seed.md"
            source.parent.mkdir(parents=True)
            source.write_text("seed", encoding="utf-8")
            update_prefetch(
                project_dir, "seed",
                [{"path": "verified/seed.md", "line_start": 1,
                  "line_end": 1, "content": "seed"}],
            )
            database = project_dir / "cache" / "global-knowledge.sqlite3"
            with closing(sqlite3.connect(database)) as connection:
                template = connection.execute(
                    "SELECT * FROM entries LIMIT 1"
                ).fetchone()
                for index in range(1, 502):
                    row = list(template)
                    row[0], row[1] = f"entry-{index}", f"verified/{index}.md"
                    connection.execute(
                        "INSERT INTO entries VALUES "
                        "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                        row,
                    )
                expected = connection.execute(
                    "SELECT COUNT(*), SUM(size_bytes) FROM entries"
                ).fetchone()
                connection.commit()

            with mock.patch(
                "project_prefetch.list_prefetch",
                side_effect=AssertionError("stats materialized cache rows"),
            ), mock.patch(
                "project_prefetch._purge_unapproved",
                side_effect=AssertionError("stats scanned all cache rows"),
            ):
                stats = prefetch_stats(project_dir)

            self.assertEqual(stats["entries"], expected[0])
            self.assertEqual(stats["used_bytes"], expected[1])


if __name__ == "__main__":
    unittest.main()
