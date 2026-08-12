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
SPEC = importlib.util.spec_from_file_location("project_tracking", SCRIPTS / "project_tracking.py")
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)

from project_prefetch import update_prefetch  # noqa: E402


class ProjectTrackingTest(unittest.TestCase):
    def test_track_project_registers_cache_without_source_index(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "repo"
            repo.mkdir()
            subprocess.run(["git", "init", str(repo)], check=True, capture_output=True)

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
            self.assertTrue((project / "cache/global-knowledge.json").is_file())


if __name__ == "__main__":
    unittest.main()
