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


if __name__ == "__main__":
    unittest.main()
