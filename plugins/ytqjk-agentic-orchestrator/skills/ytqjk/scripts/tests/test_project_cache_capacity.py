from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_intake_cli import submit_candidate  # noqa: E402
from project_prefetch import (  # noqa: E402
    enforce_project_capacity,
    list_prefetch,
    prefetch_stats,
    query_prefetch,
    update_prefetch,
)


class ProjectCacheCapacityTest(unittest.TestCase):
    @staticmethod
    def make_repo(path: Path) -> Path:
        path.mkdir()
        subprocess.run(
            ["git", "init", str(path)],
            check=True,
            capture_output=True,
        )
        return path

    def test_cache_eviction_retains_frequently_hit_entry(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            knowledge = Path(temporary) / "knowledge"
            project = knowledge / "projects" / "project"
            keep = {
                "path": "verified/keep.md",
                "line_start": 1,
                "line_end": 1,
                "content": "保留" * 10000,
            }
            discard = {
                "path": "personal-experience/approved/discard.md",
                "line_start": 1,
                "line_end": 1,
                "content": "淘汰" * 10000,
            }
            for row in (keep, discard):
                source = knowledge / str(row["path"])
                source.parent.mkdir(parents=True, exist_ok=True)
                source.write_text(str(row["content"]), encoding="utf-8")
            update_prefetch(project, "保留", [keep])
            query_prefetch(project, "保留", 5)
            query_prefetch(project, "保留", 5)
            used = prefetch_stats(project)["project_used_bytes"]
            update_prefetch(
                project,
                "淘汰",
                [discard],
                max_bytes=int(used) + 4096,
            )

            paths = [entry["path"] for entry in list_prefetch(project)]
            self.assertEqual(paths, ["verified/keep.md"])

    def test_capacity_falls_back_to_rebuildable_indexes(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            knowledge = Path(temporary) / "knowledge"
            project = knowledge / "projects" / "project"
            source = knowledge / "verified" / "cached.md"
            source.parent.mkdir(parents=True)
            source.write_text("缓存知识", encoding="utf-8")
            entry = {
                "path": "verified/cached.md",
                "line_start": 1,
                "line_end": 1,
                "content": "缓存知识",
            }
            update_prefetch(project, "缓存", [entry])
            vectors = project / "vectors"
            vectors.mkdir()
            (vectors / "index.bin").write_bytes(b"vector")
            lexical = project / "lexical.sqlite3"
            lexical.write_bytes(b"lexical")
            manifest_path = project / "manifest.json"
            manifest_path.write_text(
                '{"indexed_at":"now","vector":'
                '{"enabled":true,"status":"READY"}}',
                encoding="utf-8",
            )

            with mock.patch(
                "project_cache_capacity.directory_size",
                side_effect=[101, 101, 101, 101, 101, 101, 99],
            ):
                evicted = enforce_project_capacity(project, max_bytes=100)

            manifest = json.loads(
                manifest_path.read_text(encoding="utf-8")
            )
            self.assertEqual(evicted, ["vectors", "lexical.sqlite3"])
            self.assertFalse(vectors.exists())
            self.assertFalse(lexical.exists())
            self.assertEqual(manifest["index_state"], "EVICTED_CAPACITY")
            self.assertEqual(
                manifest["vector"]["status"],
                "EVICTED_CAPACITY",
            )

    def test_external_research_writes_global_candidate(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = self.make_repo(base / "repo")
            knowledge = base / "knowledge"

            result = submit_candidate(
                knowledge,
                repo,
                "thread-intake",
                "如何验证",
                "外部检索结论与验证步骤。",
                ["https://example.test/source"],
            )

            path = knowledge / str(result["path"])
            content = path.read_text(encoding="utf-8")
            self.assertEqual(result["state"], "CANDIDATE")
            self.assertIn(f"project_id: {result['project_id']}", content)


if __name__ == "__main__":
    unittest.main()
