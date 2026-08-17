from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SKILL = Path(__file__).resolve().parents[2]
HOOK = SKILL.parents[1] / "hooks" / "session_start.py"
SCRIPTS = SKILL / "scripts"
sys.path.insert(0, str(SCRIPTS))

from session_memory import read_anchor  # noqa: E402


class SessionStartHookTest(unittest.TestCase):
    def test_git_session_is_anchored_before_first_turn(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            project = base / "project"
            project.mkdir()
            subprocess.run(
                ["git", "init", "--quiet", str(project)], check=True
            )
            knowledge = base / "knowledge"
            event = {
                "session_id": "thread-background-anchor",
                "cwd": str(project),
                "hook_event_name": "SessionStart",
                "source": "startup",
            }
            environment = os.environ.copy()
            environment["YTQJK_KNOWLEDGE_ROOT"] = str(knowledge)

            completed = subprocess.run(
                [sys.executable, str(HOOK)],
                input=json.dumps(event),
                capture_output=True,
                text=True,
                encoding="utf-8",
                env=environment,
                check=False,
                timeout=10,
            )

            self.assertEqual(
                completed.returncode, 0, completed.stderr or completed.stdout
            )
            output = json.loads(completed.stdout)
            context = output["hookSpecificOutput"]["additionalContext"]
            self.assertIn("KNOWLEDGE_RECEIPT", context)
            self.assertIn("not only the anchor key", context)
            catalog = json.loads(
                (knowledge / "catalog.json").read_text(encoding="utf-8")
            )
            project_id = next(iter(catalog["projects"]))
            anchor = read_anchor(
                knowledge, "thread-background-anchor"
            )
            self.assertEqual(anchor["project_id"], project_id)
            self.assertNotIn("thread-background-anchor", json.dumps(anchor))

    def test_non_git_session_is_anchored_before_first_turn(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            project = base / "notes"
            project.mkdir()
            knowledge = base / "knowledge"
            event = {
                "session_id": "thread-non-git-anchor",
                "cwd": str(project),
                "hook_event_name": "SessionStart",
                "source": "startup",
            }
            environment = os.environ.copy()
            environment["YTQJK_KNOWLEDGE_ROOT"] = str(knowledge)

            completed = subprocess.run(
                [sys.executable, str(HOOK)],
                input=json.dumps(event),
                capture_output=True,
                text=True,
                encoding="utf-8",
                env=environment,
                check=False,
                timeout=10,
            )

            self.assertEqual(
                completed.returncode, 0, completed.stderr or completed.stdout
            )
            output = json.loads(completed.stdout)
            context = output["hookSpecificOutput"]["additionalContext"]
            self.assertIn("KNOWLEDGE_RECEIPT", context)
            self.assertIn("not only the anchor key", context)
            catalog = json.loads(
                (knowledge / "catalog.json").read_text(encoding="utf-8")
            )
            project_id = next(iter(catalog["projects"]))
            anchor = read_anchor(knowledge, "thread-non-git-anchor")
            self.assertEqual(anchor["project_id"], project_id)
            self.assertTrue(project_id.startswith("notes--"))


if __name__ == "__main__":
    unittest.main()
