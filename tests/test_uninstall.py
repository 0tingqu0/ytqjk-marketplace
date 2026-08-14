from __future__ import annotations

import json
import tempfile
import unittest
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path

from setup import main
from uninstall_core import apply_uninstall_plan, build_uninstall_plan


class StatefulRunner:
    def __init__(self) -> None:
        self.marketplaces = {"ytqjk"}
        self.plugins = {"ytqjk-agentic-orchestrator", "ytqjk-knowledge"}
        self.commands: list[list[str]] = []

    def __call__(self, command: list[str], _: Path) -> str:
        self.commands.append(command)
        if command[-2:] == ["list", "--json"]:
            values = (
                self.marketplaces
                if "marketplace" in command else self.plugins
            )
            return json.dumps([{"name": value} for value in values])
        if "marketplace" in command:
            self.marketplaces.discard("ytqjk")
        else:
            self.plugins.discard(command[-1].split("@", maxsplit=1)[0])
        return ""


class UninstallTest(unittest.TestCase):
    def test_all_removes_only_ytqjk_artifacts(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            for name in ("ytqjk", "caveman", "ytqjk-knowledge", "grill-me"):
                skill = target / "skills" / name / "SKILL.md"
                skill.parent.mkdir(parents=True, exist_ok=True)
                skill.write_text(name, encoding="utf-8")

            runner = StatefulRunner()
            result = apply_uninstall_plan(
                build_uninstall_plan("all", target), target, runner
            )

            self.assertEqual(result["status"], "UNINSTALLED")
            self.assertTrue(result["changed"])
            self.assertFalse(runner.marketplaces or runner.plugins)
            self.assertTrue((target / "skills" / "grill-me").is_dir())
            self.assertFalse((target / "skills" / "ytqjk").exists())
            self.assertFalse((target / "skills" / "caveman").exists())
            self.assertFalse((target / "skills" / "ytqjk-knowledge").exists())

    def test_repeated_uninstall_is_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            runner = StatefulRunner()
            apply_uninstall_plan(build_uninstall_plan("all", target), target, runner)

            repeated = apply_uninstall_plan(
                build_uninstall_plan("all", target), target, runner
            )

            self.assertFalse(repeated["changed"])
            self.assertFalse(repeated["external_commands"])

    def test_mode_limits_removal_scope(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            knowledge = target / "skills" / "ytqjk-knowledge" / "SKILL.md"
            ide = target / "skills" / "ytqjk" / "SKILL.md"
            knowledge.parent.mkdir(parents=True)
            ide.parent.mkdir(parents=True)
            knowledge.write_text("knowledge", encoding="utf-8")
            ide.write_text("ide", encoding="utf-8")

            result = apply_uninstall_plan(
                build_uninstall_plan("ide-only", target), target
            )

            self.assertTrue(result["changed"])
            self.assertTrue(knowledge.is_file())
            self.assertFalse(ide.exists())

    def test_cli_uninstall_skips_knowledge_operations(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            runner = StatefulRunner()
            output = StringIO()

            with redirect_stdout(output):
                code = main(
                    [
                        "--uninstall", "--apply", "--yes", "--json",
                        "--target-root", str(target),
                    ],
                    runner=runner,
                    codex_importer=lambda *_: self.fail("import was called"),
                )

            receipt = json.loads(output.getvalue())
            self.assertEqual(code, 0)
            self.assertEqual(receipt["operation"], "uninstall")
            self.assertEqual(receipt["apply"]["status"], "UNINSTALLED")
            self.assertEqual(
                receipt["knowledge_import"]["status"], "SKIPPED_UNINSTALL"
            )
