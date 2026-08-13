from __future__ import annotations

import json
import unittest
from pathlib import Path


REPOSITORY = Path(__file__).resolve().parents[1]
PLUGIN = REPOSITORY / "plugins" / "ytqjk-agentic-orchestrator"
SKILL = PLUGIN / "skills" / "ytqjk"


class DistributionLayoutTest(unittest.TestCase):
    def test_canonical_skill_has_no_repository_copy(self) -> None:
        named_skills = []
        for candidate in REPOSITORY.rglob("SKILL.md"):
            text = candidate.read_text(encoding="utf-8")
            if "\nname: ytqjk\n" in text:
                named_skills.append(candidate.resolve())
        self.assertEqual(named_skills, [(SKILL / "SKILL.md").resolve()])
        self.assertFalse((REPOSITORY / ".agents" / "skills" / "ytqjk").exists())
        for required in ("agents", "references", "scripts"):
            self.assertTrue((SKILL / required).is_dir())

    def test_marketplace_targets_plugin(self) -> None:
        marketplace = json.loads(
            (REPOSITORY / ".agents" / "plugins" / "marketplace.json").read_text(
                encoding="utf-8"
            )
        )
        self.assertEqual(marketplace["name"], "ytqjk")
        entry = marketplace["plugins"][0]
        self.assertEqual(entry["name"], "ytqjk-agentic-orchestrator")
        self.assertEqual(
            entry["source"]["path"],
            "./plugins/ytqjk-agentic-orchestrator",
        )
        self.assertEqual(entry["policy"]["installation"], "AVAILABLE")
        self.assertEqual(entry["policy"]["authentication"], "ON_INSTALL")
        self.assertEqual(entry["category"], "Productivity")
        manifest = json.loads(
            (PLUGIN / ".codex-plugin" / "plugin.json").read_text(encoding="utf-8")
        )
        self.assertEqual(manifest["name"], "ytqjk-agentic-orchestrator")
        self.assertRegex(manifest["version"], r"^\d+\.\d+\.\d+$")
        self.assertNotIn("+codex.", manifest["version"])
        self.assertEqual(manifest["author"]["name"], "一听曲就困")
        self.assertEqual(
            manifest["repository"],
            "https://github.com/0tingqu0/ytqjk-marketplace",
        )
        self.assertEqual(manifest["license"], "MIT")
        self.assertEqual(manifest["interface"]["developerName"], "一听曲就困")
        prompt = manifest["interface"]["defaultPrompt"]
        self.assertLessEqual(len(prompt.encode("utf-8")), 128)
        self.assertIn("$ytqjk", prompt)
        self.assertIn("GOAL_INTAKE", prompt)
        self.assertIn("make no tool call", prompt)
        self.assertIn("until explicit confirmation", prompt)
        agent_metadata = (SKILL / "agents" / "openai.yaml").read_text(
            encoding="utf-8"
        )
        self.assertIn(f'default_prompt: "{prompt}"', agent_metadata)

    def test_readme_installs_canonical_skill_for_ide(self) -> None:
        readme = (REPOSITORY / "README.md").read_text(encoding="utf-8")
        expected_url = (
            "https://github.com/0tingqu0/ytqjk-marketplace/tree/main/"
            "plugins/ytqjk-agentic-orchestrator/skills"
        )
        self.assertIn(expected_url, readme)
        self.assertIn(
            "--agent codex --skill ytqjk --skill caveman --copy", readme
        )
        self.assertIn("$ytqjk", readme)
        self.assertIn("/skills", readme)
        self.assertIn("完整多会话编排", readme)
        self.assertIn("Codex CLI 输入 `/plugins`", readme)
        self.assertIn("codex plugin add ytqjk-agentic-orchestrator@ytqjk", readme)
        self.assertIn("npx skills@latest update ytqjk caveman -p", readme)
        self.assertIn("目标明确并由你显式确认前", readme)

    def test_bundled_caveman_has_attribution_and_license(self) -> None:
        caveman = PLUGIN / "skills" / "caveman" / "SKILL.md"
        license_path = PLUGIN / "skills" / "caveman" / "LICENSE"
        notices = PLUGIN / "THIRD_PARTY_NOTICES.md"
        self.assertTrue(caveman.is_file())
        self.assertIn("Matt Pocock", caveman.read_text(encoding="utf-8"))
        self.assertIn(
            "Copyright (c) 2026 Matt Pocock",
            license_path.read_text(encoding="utf-8"),
        )
        notice_text = notices.read_text(encoding="utf-8")
        self.assertIn("Copyright (c) 2026 Matt Pocock", notice_text)
        self.assertIn("MIT License", notice_text)

    def test_ytqjk_activation_is_zero_tool_and_deferred(self) -> None:
        skill_text = (SKILL / "SKILL.md").read_text(encoding="utf-8")
        self.assertLessEqual(len(skill_text.encode("utf-8")), 2500)
        normalized = " ".join(skill_text.split())
        instant = normalized.index("## Activation objective gate")
        deferred = normalized.index("## Deferred initialization")
        self.assertLess(instant, deferred)
        instant_contract = normalized[instant:deferred]
        self.assertIn(
            "Throughout `GOAL_INTAKE`, before explicit objective confirmation, make no tool call",
            instant_contract,
        )
        self.assertIn("stay in the current activation task", instant_contract)
        self.assertIn(
            "Ask exactly one objective question per response", instant_contract
        )
        self.assertIn(
            "only an affirmative reply to that summary counts", instant_contract
        )
        self.assertIn(
            "controller, supervisor, progress, RAG, reviewer, Git, or Worker",
            instant_contract,
        )
        self.assertNotIn("$caveman", instant_contract)
        self.assertIn(
            "Only after explicit objective confirmation, make reading",
            normalized,
        )
        self.assertIn("On explicit stop or pause, send the stop once", normalized)
        self.assertNotIn("Before creating any task, read", normalized)

        protocol = (SKILL / "references" / "protocol.md").read_text(
            encoding="utf-8"
        )
        protocol_normalized = " ".join(protocol.split())
        self.assertIn(
            "Throughout `GOAL_INTAKE`, before explicit objective confirmation, make no tool call",
            protocol_normalized,
        )
        self.assertIn(
            "all objective clarification in the current activation task",
            protocol_normalized,
        )
        self.assertIn("Invocation alone does not", protocol_normalized)
        self.assertIn(
            "Objective confirmation does not approve the plan",
            protocol_normalized,
        )

    def test_protocol_uses_jit_roles_and_bounded_recovery(self) -> None:
        skill = (SKILL / "SKILL.md").read_text(encoding="utf-8")
        protocol = (SKILL / "references" / "protocol.md").read_text(
            encoding="utf-8"
        )
        normalized = " ".join(protocol.split())

        self.assertIn("host-tool-driven", skill)
        self.assertIn("not a background daemon", skill)
        self.assertIn(
            "create, list, read, wait, message, and title operations", normalized
        )
        self.assertIn("Pin and archive are optional enhancements", normalized)
        self.assertIn("Read-only objectives never create it", normalized)
        self.assertIn("No result means no reviewer conversation", normalized)
        self.assertIn("Non-Git read-only work may continue", normalized)
        self.assertIn("`NON_GIT_MUTATION` plan is `BLOCKED`", normalized)
        self.assertIn("idempotent run token", normalized)
        self.assertIn(
            "An ambiguous delivery after the bounded retry is `BLOCKED`", normalized
        )
        self.assertIn("### Read-only loop", protocol)
        self.assertIn("### Git mutation loop", protocol)
        self.assertIn("mark it `DONE`; a host lifecycle action", normalized)
        self.assertNotIn("Both must exist before Worker dispatch", protocol)
        self.assertNotIn("list, read, wait, message, title, pin, and archive", protocol)
        self.assertIn("Never overwrite a different active objective", normalized)

        archive_rules = normalized[normalized.index("## 6. Status and archive rules") :]
        checkpoint = archive_rules.index("session_memory.py checkpoint")
        memory_archive = archive_rules.index("session_memory.py archive")
        host_archive = archive_rules.index("archive the host conversation")
        self.assertLess(checkpoint, memory_archive)
        self.assertLess(memory_archive, host_archive)

    def test_repository_has_no_generated_python_cache(self) -> None:
        generated = [
            path
            for path in REPOSITORY.rglob("*")
            if path.name == "__pycache__" or path.suffix in {".pyc", ".pyo"}
        ]
        self.assertEqual(generated, [])

    def test_rag_workflow_refreshes_before_size_gated_vectors(self) -> None:
        protocol = (SKILL / "references" / "protocol.md").read_text(
            encoding="utf-8"
        )
        knowledge = (SKILL / "references" / "knowledge-store.md").read_text(
            encoding="utf-8"
        )
        normalized_protocol = " ".join(protocol.split())
        normalized_knowledge = " ".join(knowledge.split())
        self.assertIn("Before the first query", normalized_protocol)
        self.assertIn(
            "missing, stale, or security-incompatible index", normalized_protocol
        )
        self.assertIn("vectors are size-gated only", normalized_protocol)
        self.assertIn(
            "Repeated low-confidence queries never download", normalized_knowledge
        )
        self.assertIn(
            "report that result instead of repeating queries", normalized_knowledge
        )

    def test_local_knowledge_dashboard_is_packaged(self) -> None:
        dashboard = SKILL / "dashboard"
        for name in ("knowledge_dashboard.py", "index.html", "app.js", "style.css"):
            self.assertTrue((dashboard / name).is_file())
        knowledge = (SKILL / "references" / "knowledge-store.md").read_text(
            encoding="utf-8"
        )
        self.assertIn("## Local management dashboard", knowledge)
        self.assertIn("binds only to `127.0.0.1`", knowledge)
        self.assertIn("explicitly approved by the user", knowledge)
        self.assertIn("never auto-approves candidates", knowledge)


if __name__ == "__main__":
    unittest.main()
