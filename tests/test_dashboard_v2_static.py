from __future__ import annotations

import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DASHBOARD = ROOT / "plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard"


class DashboardV2StaticTest(unittest.TestCase):
    def test_workbench_exposes_six_single_view_routes(self) -> None:
        html = (DASHBOARD / "index.html").read_text(encoding="utf-8")

        for route in (
            "overview",
            "intake",
            "documents",
            "review",
            "libraries",
            "sessions",
        ):
            self.assertIn(f'data-view="{route}"', html)
            self.assertIn(f'data-route="{route}"', html)

        for element_id in (
            "version-trigger",
            "update-panel",
            "install-update",
            "refresh",
            "open-global-library",
        ):
            self.assertIn(f'id="{element_id}"', html)

    def test_v2_uses_plain_surfaces_and_explicit_tree_boundary(self) -> None:
        html = (DASHBOARD / "index.html").read_text(encoding="utf-8")
        style = (DASHBOARD / "style.css").read_text(encoding="utf-8")
        libraries = (DASHBOARD / "js/views/libraries.js").read_text(
            encoding="utf-8"
        )

        self.assertNotIn("gradient", style)
        self.assertNotIn("backdrop-filter", style)
        self.assertNotIn("@keyframes", style)
        self.assertIn("[hidden] { display: none !important; }", style)
        self.assertIn("max-width: 900px", style)
        self.assertIn("max-width: 600px", style)
        self.assertIn("NOT_CONFIGURED", libraries)
        intake = (DASHBOARD / "js/views/intake.js").read_text(
            encoding="utf-8"
        )
        self.assertNotIn("20%", intake)
        self.assertIn('type="module" src="app.js"', html)

    def test_entry_files_stay_small_and_api_contract_is_centralized(
        self,
    ) -> None:
        app = (DASHBOARD / "app.js").read_text(encoding="utf-8")
        index = (DASHBOARD / "index.html").read_text(encoding="utf-8")
        api = (DASHBOARD / "js/api.js").read_text(encoding="utf-8")

        self.assertLessEqual(len(app.splitlines()), 200)
        self.assertLessEqual(len(index.splitlines()), 200)
        for endpoint in (
            "/api/snapshot",
            "/api/document",
            "/api/candidate",
            "/api/intake",
            "/api/project-library",
        ):
            self.assertIn(endpoint, api)

    def test_periodic_refresh_preserves_candidate_drafts(self) -> None:
        app = (DASHBOARD / "app.js").read_text(encoding="utf-8")
        store = (DASHBOARD / "js/store.js").read_text(encoding="utf-8")
        documents = (DASHBOARD / "js/views/documents.js").read_text(
            encoding="utf-8"
        )
        review = (DASHBOARD / "js/views/review.js").read_text(
            encoding="utf-8"
        )

        self.assertIn("drafts: new Map()", store)
        self.assertIn("已保留未保存草稿", app)
        self.assertIn("state.drafts.set", documents)
        self.assertIn("state.drafts.set", review)
        self.assertIn("state.drafts.delete", app)

    def test_mobile_targets_and_dialog_backdrops_are_safe(self) -> None:
        style = (DASHBOARD / "style.css").read_text(encoding="utf-8")
        rail = (DASHBOARD / "js/ui/rail.js").read_text(encoding="utf-8")
        command = (DASHBOARD / "js/command-palette.js").read_text(
            encoding="utf-8"
        )
        confirm = (DASHBOARD / "js/ui/confirm.js").read_text(
            encoding="utf-8"
        )

        self.assertIn("button, .secondary, input, summary,", style)
        self.assertIn(".tree-form-grid select", style)
        self.assertIn("min-height: 44px", style)
        self.assertIn(".app-shell.rail-open::after", style)
        self.assertIn("pointer-events: none", style)
        self.assertIn("event.target.closest?.(allowed)", rail)
        self.assertIn("#app-rail, #rail-toggle, #bottom-more", rail)
        self.assertIn("event.target === event.currentTarget", command)
        self.assertIn("event.target === event.currentTarget", confirm)
        self.assertIn('dialog.close("cancel")', confirm)


if __name__ == "__main__":
    unittest.main()
