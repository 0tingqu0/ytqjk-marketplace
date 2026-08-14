from __future__ import annotations

import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class DashboardDocumentationTest(unittest.TestCase):
    def test_readme_uses_stable_dashboard_path_and_loopback_contract(self) -> None:
        readme = (ROOT / "README.md").read_text(encoding="utf-8")
        dashboard = (
            ROOT / "plugins" / "ytqjk-agentic-orchestrator" / "skills"
            / "ytqjk" / "dashboard" / "knowledge_dashboard.py"
        ).read_text(encoding="utf-8")
        stable = "~/.codex/plugins/ytqjk-agentic-orchestrator"
        self.assertIn(stable, readme)
        self.assertNotIn(
            "python3 plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard",
            readme,
        )
        self.assertIn('url.path == "/api/snapshot"', dashboard)
        self.assertIn('ThreadingHTTPServer(("127.0.0.1", args.port)', dashboard)
