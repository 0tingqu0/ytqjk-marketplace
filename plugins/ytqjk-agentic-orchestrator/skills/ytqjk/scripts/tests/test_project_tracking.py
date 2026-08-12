from __future__ import annotations

import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location("project_tracking", SCRIPTS / "project_tracking.py")
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

from global_session_query import query_global  # noqa: E402
from knowledge_intake_cli import submit_candidate  # noqa: E402
from project_prefetch import (  # noqa: E402
    CACHE_NAME,
    MAX_PROJECT_CACHE_BYTES,
    enforce_project_capacity,
    list_prefetch,
    prefetch_stats,
    query_prefetch,
    update_prefetch,
)
from rag_common import Chunk, SCHEMA_VERSION, atomic_json, build_lexical, utc_now  # noqa: E402


class ProjectTrackingTest(unittest.TestCase):
    def make_repo(self, path: Path) -> Path:
        path.mkdir()
        subprocess.run(["git", "init", str(path)], check=True, capture_output=True)
        return path

    def make_global_index(self, knowledge: Path, content: str = "") -> None:
        cache = knowledge / "global-cache"
        cache.mkdir(parents=True, exist_ok=True)
        chunks = []
        if content:
            chunks.append(Chunk("id-1", "verified/fact.md", 1, 2, content, "hash", utc_now(), "GLOBAL"))
        build_lexical(cache / "lexical.sqlite3", chunks)
        atomic_json(cache / "manifest.json", {"schema_version": SCHEMA_VERSION, "indexed_at": utc_now()})

    def test_track_project_registers_cache_without_source_index(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")

            tracked = MODULE.track_project(base / "knowledge", repo)
            catalog = json.loads((base / "knowledge/catalog.json").read_text(encoding="utf-8"))

            self.assertTrue((base / "knowledge/projects" / tracked["id"] / "cache").is_dir())
            self.assertEqual(catalog["projects"][tracked["id"]]["tracking_state"], "REGISTERED")
            self.assertFalse((base / "knowledge/projects" / tracked["id"] / "lexical.sqlite3").exists())

    def test_prefetch_cache_is_rebuildable_project_state(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            project = Path(temporary) / "project"
            entries = update_prefetch(project, "部署", [{"path": "personal-experience/approved/lesson.md", "line_start": 1, "line_end": 2, "content": "部署验证步骤。"}])

            self.assertEqual(entries[0]["query"], "部署")
            self.assertTrue((project / "cache" / CACHE_NAME).is_file())
            self.assertEqual(prefetch_stats(project)["capacity_bytes"], MAX_PROJECT_CACHE_BYTES)
            self.assertFalse(prefetch_stats(project)["capacity_exceeded"])

    def test_project_cache_hit_stops_before_global_lookup(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")
            knowledge = base / "knowledge"
            tracked = MODULE.track_project(knowledge, repo)
            update_prefetch(knowledge / "projects" / tracked["id"], "部署", [{"path": "verified/fact.md", "line_start": 1, "line_end": 1, "content": "部署缓存知识"}])

            result = query_global(knowledge, repo, "部署缓存", "thread-cache", 5)

            self.assertEqual(result["status"], "PROJECT_CACHE_HIT")
            self.assertEqual(result["scope"], "current-project-cache-only")
            self.assertEqual(result["cache"]["policy"], "LFU_LRU")

    def test_project_source_index_hit_stops_before_global_lookup(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")
            knowledge = base / "knowledge"
            tracked = MODULE.track_project(knowledge, repo)
            project = knowledge / "projects" / tracked["id"]
            chunks = [
                Chunk("project-1", "src/cache.py", 1, 2, "项目子库源码知识", "hash", utc_now(), "HEAD")
            ]
            build_lexical(project / "lexical.sqlite3", chunks)
            atomic_json(
                project / "manifest.json",
                {"schema_version": SCHEMA_VERSION, "indexed_at": utc_now()},
            )

            result = query_global(knowledge, repo, "项目子库", "thread-source", 5)

            self.assertEqual(result["status"], "PROJECT_CACHE_HIT")
            self.assertEqual(result["results"][0]["scope"], "project-source-cache")

    def test_global_fallback_fills_only_current_project_cache(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo_a = self.make_repo(base / "repo-a")
            repo_b = self.make_repo(base / "repo-b")
            knowledge = base / "knowledge"
            self.make_global_index(knowledge, "总库回源知识")

            first = query_global(knowledge, repo_a, "总库回源", "thread-a", 5)
            second = query_global(knowledge, repo_a, "总库回源", "thread-a", 5)
            tracked_b = MODULE.track_project(knowledge, repo_b)

            self.assertEqual(first["status"], "GLOBAL_FALLBACK_HIT")
            self.assertEqual(second["status"], "PROJECT_CACHE_HIT")
            self.assertEqual(list_prefetch(knowledge / "projects" / tracked_b["id"]), [])

    def test_total_miss_returns_external_research_signal(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")
            knowledge = base / "knowledge"
            self.make_global_index(knowledge)

            result = query_global(knowledge, repo, "不存在的知识", "thread-miss", 5)

            self.assertEqual(result["status"], "KNOWLEDGE_MISS")
            self.assertEqual(result["next_action"], "SEARCH_EXTERNAL_THEN_SUBMIT_CANDIDATE")

    def test_cross_project_session_is_rejected_before_target_registration(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo_a = self.make_repo(base / "repo-a")
            repo_b = self.make_repo(base / "repo-b")
            knowledge = base / "knowledge"
            self.make_global_index(knowledge)
            query_global(knowledge, repo_a, "初次查询", "thread-bound", 5)
            project_b = MODULE.identify_project(repo_b)

            with self.assertRaisesRegex(ValueError, "禁止访问其他项目子库"):
                query_global(knowledge, repo_b, "跨项目查询", "thread-bound", 5)

            catalog = json.loads((knowledge / "catalog.json").read_text(encoding="utf-8"))
            self.assertNotIn(project_b["id"], catalog["projects"])
            self.assertFalse((knowledge / "projects" / project_b["id"]).exists())

    def test_cache_eviction_retains_frequently_hit_entry(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            project = Path(temporary) / "project"
            keep = {"path": "keep.md", "line_start": 1, "line_end": 1, "content": "保留" * 10000}
            discard = {"path": "discard.md", "line_start": 1, "line_end": 1, "content": "淘汰" * 10000}
            update_prefetch(project, "保留", [keep])
            query_prefetch(project, "保留", 5)
            query_prefetch(project, "保留", 5)
            one_entry_size = prefetch_stats(project)["project_used_bytes"]
            update_prefetch(project, "淘汰", [discard], max_bytes=int(one_entry_size) + 4096)

            paths = [entry["path"] for entry in list_prefetch(project)]
            self.assertEqual(paths, ["keep.md"])

    def test_capacity_falls_back_to_rebuildable_indexes(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            project = Path(temporary) / "project"
            entry = {"path": "cached.md", "line_start": 1, "line_end": 1, "content": "缓存知识"}
            update_prefetch(project, "缓存", [entry])
            vectors = project / "vectors"
            vectors.mkdir()
            (vectors / "index.bin").write_bytes(b"vector")
            (project / "lexical.sqlite3").write_bytes(b"lexical")
            (project / "manifest.json").write_text(
                '{"indexed_at":"now","vector":{"enabled":true,"status":"READY"}}',
                encoding="utf-8",
            )

            with mock.patch(
                "project_prefetch._directory_size",
                side_effect=[101, 101, 101, 101, 101, 101, 99],
            ):
                evicted = enforce_project_capacity(project, max_bytes=100)

            manifest = json.loads((project / "manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(evicted, ["vectors", "lexical.sqlite3"])
            self.assertFalse(vectors.exists())
            self.assertFalse((project / "lexical.sqlite3").exists())
            self.assertEqual(manifest["index_state"], "EVICTED_CAPACITY")
            self.assertEqual(manifest["vector"]["status"], "EVICTED_CAPACITY")

    def test_external_research_writes_global_candidate(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")
            knowledge = base / "knowledge"

            result = submit_candidate(
                knowledge, repo, "thread-intake", "如何验证", "外部检索结论与验证步骤。", ["https://example.test/source"],
            )

            content = (knowledge / str(result["path"])).read_text(encoding="utf-8")
            self.assertEqual(result["state"], "CANDIDATE")
            self.assertIn(f"project_id: {result['project_id']}", content)


if __name__ == "__main__":
    unittest.main()
