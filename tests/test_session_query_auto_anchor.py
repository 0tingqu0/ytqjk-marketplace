from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = (
    ROOT
    / "plugins"
    / "ytqjk-agentic-orchestrator"
    / "skills"
    / "ytqjk"
    / "scripts"
    / "session_query.py"
)


class SessionQueryAutoAnchorTest(unittest.TestCase):
    def test_missing_expected_project_id_is_derived_from_git_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            project = base / "project"
            project.mkdir()
            subprocess.run(
                ["git", "init", "--quiet", str(project)], check=True
            )
            knowledge = base / "knowledge"
            session_id = "auto-guidance-session"

            completed = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "find project knowledge",
                    "--knowledge-root",
                    str(knowledge),
                    "--project-root",
                    str(project),
                    "--session-id",
                    session_id,
                    "--limit",
                    "5",
                ],
                capture_output=True,
                text=True,
                encoding="utf-8",
                check=False,
                timeout=20,
            )

            self.assertEqual(
                completed.returncode, 0, completed.stderr or completed.stdout
            )
            result = json.loads(completed.stdout)
            self.assertEqual(result["status"], "KNOWLEDGE_MISS")
            self.assertEqual(result["project_tracking"], "REGISTERED")
            self.assertTrue(result["anchor_created"])
            anchors = list((knowledge / "sessions").glob("*/anchor.json"))
            self.assertEqual(len(anchors), 1)
            self.assertNotIn(session_id, anchors[0].read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
