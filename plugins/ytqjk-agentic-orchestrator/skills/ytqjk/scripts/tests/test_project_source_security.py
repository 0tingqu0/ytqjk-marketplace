from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from project_source import tracked_paths  # noqa: E402
from rag_common import DEFAULT_CONFIG, scan_project  # noqa: E402


def make_directory_junction(link: Path, target: Path) -> None:
    result = subprocess.run(
        ["cmd.exe", "/d", "/c", "mklink", "/J", str(link), str(target)],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise OSError(result.stderr or result.stdout)


class ProjectSourceSecurityTest(unittest.TestCase):
    def test_regular_nested_file_remains_indexable(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            project = Path(temporary) / "project"
            nested = project / "docs" / "nested"
            nested.mkdir(parents=True)
            (nested / "guide.md").write_text("TRUSTED_NESTED", encoding="utf-8")

            paths = tracked_paths(project)
            chunks, stats = scan_project(project, DEFAULT_CONFIG, "NON_GIT")

            self.assertEqual(paths, ["docs/nested/guide.md"])
            self.assertEqual(stats["files"], 1)
            self.assertEqual([chunk.content for chunk in chunks], ["TRUSTED_NESTED"])

    def test_directory_symlink_is_not_followed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            project = base / "project"
            outside = base / "outside"
            project.mkdir()
            outside.mkdir()
            (outside / "fact.md").write_text("OUTSIDE_SYMLINK", encoding="utf-8")
            link = project / "linked"
            try:
                link.symlink_to(outside, target_is_directory=True)
            except OSError as error:
                self.skipTest(f"directory symlink unavailable: {error}")

            self.assertEqual(tracked_paths(project), [])

    @unittest.skipUnless(os.name == "nt", "Windows junction regression")
    def test_non_git_junction_cannot_escape_project_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            project = base / "project"
            outside = base / "outside"
            project.mkdir()
            outside.mkdir()
            (outside / "fact.md").write_text("OUTSIDE_JUNCTION", encoding="utf-8")
            make_directory_junction(project / "linked", outside)

            paths = tracked_paths(project)
            chunks, stats = scan_project(project, DEFAULT_CONFIG, "NON_GIT")

            self.assertEqual(paths, [])
            self.assertEqual(chunks, [])
            self.assertEqual(stats["files"], 0)


if __name__ == "__main__":
    unittest.main()
