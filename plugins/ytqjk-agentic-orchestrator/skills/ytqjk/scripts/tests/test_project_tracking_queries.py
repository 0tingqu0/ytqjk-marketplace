from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location(
    "project_tracking",
    SCRIPTS / "project_tracking.py",
)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

from global_session_query import query_global  # noqa: E402
from global_store import chunks_fingerprint, scan_global  # noqa: E402
from project_prefetch import (  # noqa: E402
    list_prefetch,
    query_prefetch,
    update_prefetch,
)
from rag_common import (  # noqa: E402
    DEFAULT_CONFIG,
    Chunk,
    SCHEMA_VERSION,
    atomic_json,
    build_lexical,
    config_fingerprint,
    utc_now,
)


class ProjectTrackingQueryTest(unittest.TestCase):
    def make_repo(self, path: Path) -> Path:
        path.mkdir()
        subprocess.run(
            ["git", "init", str(path)],
            check=True,
            capture_output=True,
        )
        return path

    def make_global_index(
        self,
        knowledge: Path,
        content: str = "",
    ) -> None:
        cache = knowledge / "global-cache"
        cache.mkdir(parents=True, exist_ok=True)
        if content:
            verified = knowledge / "verified"
            verified.mkdir(parents=True, exist_ok=True)
            (verified / "fact.md").write_text(content, encoding="utf-8")
        chunks, stats = scan_global(knowledge, DEFAULT_CONFIG)
        build_lexical(cache / "lexical.sqlite3", chunks)
        generation = chunks_fingerprint(chunks)
        atomic_json(
            cache / "manifest.json",
            {
                "schema_version": SCHEMA_VERSION,
                "indexed_at": utc_now(),
                "source_fingerprint": generation,
                "generation": generation,
                "config_fingerprint": config_fingerprint(DEFAULT_CONFIG),
                "stats": stats,
                "vector_mode": "off",
                "vector": {"enabled": False, "status": "DISABLED"},
            },
        )

    def test_project_cache_hit_stops_before_global_lookup(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")
            knowledge = base / "knowledge"
            tracked = MODULE.track_project(knowledge, repo)
            verified = knowledge / "verified"
            verified.mkdir(parents=True)
            (verified / "fact.md").write_text(
                "部署缓存知识",
                encoding="utf-8",
            )
            update_prefetch(
                knowledge / "projects" / tracked["id"],
                "部署",
                [
                    {
                        "path": "verified/fact.md",
                        "line_start": 1,
                        "line_end": 1,
                        "content": "部署缓存知识",
                    }
                ],
                generation="GLOBAL_INDEX_ABSENT",
            )

            result = query_global(
                knowledge,
                repo,
                "部署缓存",
                "thread-cache",
                tracked["id"],
                5,
            )

            self.assertEqual(result["status"], "PROJECT_CACHE_HIT")
            self.assertEqual(
                result["scope"],
                "current-project-cache-only",
            )
            self.assertEqual(result["cache"]["policy"], "LFU_LRU")

    def test_prefetch_rejects_stale_citation_lines(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            knowledge = base / "knowledge"
            project = knowledge / "projects" / "project-one"
            verified = knowledge / "verified"
            verified.mkdir(parents=True)
            source = verified / "fact.md"
            source.write_text(
                "CACHED_MARKER\nsecond line",
                encoding="utf-8",
            )
            update_prefetch(
                project,
                "CACHED_MARKER",
                [
                    {
                        "path": "verified/fact.md",
                        "line_start": 1,
                        "line_end": 1,
                        "content": "CACHED_MARKER",
                    }
                ],
            )
            self.assertTrue(
                query_prefetch(
                    project,
                    "CACHED_MARKER",
                    5,
                    knowledge_root=knowledge,
                )
            )

            source.write_text(
                "inserted one\ninserted two\nCACHED_MARKER\nsecond line",
                encoding="utf-8",
            )
            stale = query_prefetch(
                project,
                "CACHED_MARKER",
                5,
                knowledge_root=knowledge,
            )

            self.assertEqual(stale, [])
            self.assertEqual(list_prefetch(project), [])

    def test_project_source_hit_stops_before_global_lookup(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")
            knowledge = base / "knowledge"
            tracked = MODULE.track_project(knowledge, repo)
            project = knowledge / "projects" / tracked["id"]
            chunks = [
                Chunk(
                    "project-1",
                    "src/cache.py",
                    1,
                    2,
                    "项目子库源码知识",
                    "hash",
                    utc_now(),
                    "HEAD",
                )
            ]
            build_lexical(project / "lexical.sqlite3", chunks)
            atomic_json(
                project / "manifest.json",
                {
                    "schema_version": SCHEMA_VERSION,
                    "indexed_at": utc_now(),
                },
            )

            result = query_global(
                knowledge,
                repo,
                "项目子库",
                "thread-source",
                tracked["id"],
                5,
            )

            self.assertEqual(result["status"], "PROJECT_CACHE_HIT")
            self.assertEqual(
                result["results"][0]["scope"],
                "project-source-cache",
            )

    def test_global_fallback_fills_only_current_project_cache(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo_a = self.make_repo(base / "repo-a")
            repo_b = self.make_repo(base / "repo-b")
            knowledge = base / "knowledge"
            self.make_global_index(knowledge, "总库回源知识")

            project_a = MODULE.identify_project(repo_a)
            first = query_global(
                knowledge,
                repo_a,
                "总库回源",
                "thread-a",
                project_a["id"],
                5,
            )
            second = query_global(
                knowledge,
                repo_a,
                "总库回源",
                "thread-a",
                project_a["id"],
                5,
            )
            tracked_b = MODULE.track_project(knowledge, repo_b)

            self.assertEqual(first["status"], "GLOBAL_FALLBACK_HIT")
            self.assertEqual(second["status"], "PROJECT_CACHE_HIT")
            other = knowledge / "projects" / tracked_b["id"]
            self.assertEqual(list_prefetch(other), [])

    def test_total_miss_returns_external_research_signal(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")
            knowledge = base / "knowledge"
            self.make_global_index(knowledge)

            project = MODULE.identify_project(repo)
            result = query_global(
                knowledge,
                repo,
                "不存在的知识",
                "thread-miss",
                project["id"],
                5,
            )

            self.assertEqual(result["status"], "KNOWLEDGE_MISS")
            self.assertEqual(
                result["next_action"],
                "SEARCH_EXTERNAL_THEN_SUBMIT_CANDIDATE",
            )

    def test_cross_project_session_rejected_before_registration(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo_a = self.make_repo(base / "repo-a")
            repo_b = self.make_repo(base / "repo-b")
            knowledge = base / "knowledge"
            self.make_global_index(knowledge)
            project_a = MODULE.identify_project(repo_a)
            query_global(
                knowledge,
                repo_a,
                "初次查询",
                "thread-bound",
                project_a["id"],
                5,
            )
            project_b = MODULE.identify_project(repo_b)

            with self.assertRaisesRegex(
                ValueError,
                "禁止访问其他项目子库",
            ):
                query_global(
                    knowledge,
                    repo_b,
                    "跨项目查询",
                    "thread-bound",
                    project_b["id"],
                    5,
                )

            catalog_path = knowledge / "catalog.json"
            catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
            self.assertNotIn(project_b["id"], catalog["projects"])
            other = knowledge / "projects" / project_b["id"]
            self.assertFalse(other.exists())

if __name__ == "__main__":
    unittest.main()
