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
        self.assertIn('url.path == "/api/update"', dashboard)
        self.assertIn('urlparse(self.path).path == "/api/update"', dashboard)
        self.assertIn('ThreadingHTTPServer(("127.0.0.1", args.port)', dashboard)

    def test_dashboard_exposes_release_update_control(self) -> None:
        dashboard = (
            ROOT / "plugins" / "ytqjk-agentic-orchestrator" / "skills"
            / "ytqjk" / "dashboard"
        )
        html = (dashboard / "index.html").read_text(encoding="utf-8")
        script = (dashboard / "update.js").read_text(encoding="utf-8")

        self.assertIn('id="install-update"', html)
        self.assertIn('<script src="update.js"></script>', html)
        self.assertIn('fetch("/api/update"', script)
        self.assertIn('method: "POST"', script)
