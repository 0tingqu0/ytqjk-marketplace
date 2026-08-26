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
    "project_tracking", SCRIPTS / "project_tracking.py"
)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

from global_session_query import query_global  # noqa: E402
from global_store import chunks_fingerprint, scan_global  # noqa: E402
from project_prefetch import (  # noqa: E402
    CACHE_NAME,
    MAX_PROJECT_CACHE_BYTES,
    prefetch_stats,
    update_prefetch,
)
from rag_common import (  # noqa: E402
    DEFAULT_CONFIG,
    SCHEMA_VERSION,
    atomic_json,
    build_lexical,
    config_fingerprint,
    scan_project,
    utc_now,
)


class ProjectTrackingTest(unittest.TestCase):
    def make_repo(self, path: Path) -> Path:
        path.mkdir()
        subprocess.run(
            ["git", "init", str(path)],
            check=True,
            capture_output=True,
        )
        return path

    def make_global_index(self, knowledge: Path, content: str = "") -> None:
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

    def test_track_project_registers_cache_without_source_index(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")

            tracked = MODULE.track_project(base / "knowledge", repo)
            catalog_path = base / "knowledge/catalog.json"
            catalog = json.loads(catalog_path.read_text(encoding="utf-8"))

            project = base / "knowledge/projects" / tracked["id"]
            self.assertTrue((project / "cache").is_dir())
            self.assertEqual(
                catalog["projects"][tracked["id"]]["tracking_state"],
                "REGISTERED",
            )
            self.assertFalse((project / "lexical.sqlite3").exists())

    def test_non_git_directory_registers_sub_library_and_supports_queries(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            workspace = base / "notes workspace"
            workspace.mkdir()
            (workspace / "guide.md").write_text(
                "普通目录也可以建立项目知识索引。",
                encoding="utf-8",
            )
            knowledge = base / "knowledge"
            self.make_global_index(knowledge, "总库知识")

            identified = MODULE.identify_project(workspace)
            chunks, stats = scan_project(workspace, DEFAULT_CONFIG, "NON_GIT")
            result = query_global(
                knowledge, workspace, "总库知识", "non-git-session",
                identified["id"], 5,
            )

            self.assertTrue(identified["id"].startswith("notes-workspace--"))
            self.assertEqual(stats["files"], 1)
            self.assertEqual(chunks[0].path, "guide.md")
            self.assertEqual(result["status"], "GLOBAL_FALLBACK_HIT")
            cache = knowledge / "projects" / identified["id"] / "cache"
            anchors = list((knowledge / "sessions").glob("*/anchor.json"))
            self.assertTrue(cache.is_dir())
            self.assertEqual(len(anchors), 1)

    def test_p2604_uses_existing_stable_project_id(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            repo = self.make_repo(Path(temporary) / "p2604_soc")

            identified = MODULE.identify_project(repo)

            self.assertEqual(identified["id"], "p2604_soc")

    def test_project_id_uses_local_project_name_with_remote_identity_hash(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            repo = self.make_repo(Path(temporary) / "agentic")
            subprocess.run(
                [
                    "git", "-C", str(repo), "remote", "add", "origin",
                    "https://example.test/ytqjk-marketplace.git",
                ],
                check=True,
                capture_output=True,
            )

            identified = MODULE.identify_project(repo)

            self.assertEqual(identified["name"], "agentic")
            self.assertTrue(identified["id"].startswith("agentic--"))

    def test_prefetch_cache_is_rebuildable_project_state(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            knowledge = Path(temporary) / "knowledge"
            project = knowledge / "projects" / "project"
            source = (
                knowledge
                / "personal-experience"
                / "approved"
                / "lesson.md"
            )
            source.parent.mkdir(parents=True)
            source.write_text("部署验证步骤。", encoding="utf-8")
            entries = update_prefetch(
                project,
                "部署",
                [{
                    "path": (
                        "personal-experience/approved/lesson.md"
                    ),
                    "line_start": 1,
                    "line_end": 1,
                    "content": "部署验证步骤。",
                }],
            )

            self.assertEqual(entries[0]["query"], "部署")
            self.assertTrue((project / "cache" / CACHE_NAME).is_file())
            self.assertEqual(
                prefetch_stats(project)["capacity_bytes"],
                MAX_PROJECT_CACHE_BYTES,
            )
            self.assertFalse(prefetch_stats(project)["capacity_exceeded"])

if __name__ == "__main__":
    unittest.main()
