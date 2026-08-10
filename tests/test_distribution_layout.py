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
        self.assertEqual(
            marketplace["plugins"][0]["source"]["path"],
            "./plugins/ytqjk-agentic-orchestrator",
        )
        manifest = json.loads(
            (PLUGIN / ".codex-plugin" / "plugin.json").read_text(encoding="utf-8")
        )
        self.assertEqual(manifest["name"], "ytqjk-agentic-orchestrator")
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

    def test_repository_has_no_generated_python_cache(self) -> None:
        generated = [
            path
            for path in REPOSITORY.rglob("*")
            if path.name == "__pycache__" or path.suffix in {".pyc", ".pyo"}
        ]
        self.assertEqual(generated, [])


if __name__ == "__main__":
    unittest.main()
