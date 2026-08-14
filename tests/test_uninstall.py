from __future__ import annotations

import json
import tempfile
import unittest
from contextlib import redirect_stdout
from io import StringIO
from pathlib import Path
from unittest import mock

from install_external import InstallError
from setup import main
from install_external_codex import materialize_plugins
import uninstall_core
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


class FailingRemovalRunner(StatefulRunner):
    def __call__(self, command: list[str], _: Path) -> str:
        self.commands.append(command)
        if command[-2:] == ["list", "--json"]:
            values = (
                self.marketplaces
                if "marketplace" in command else self.plugins
            )
            return json.dumps([{"name": value} for value in values])
        if command[-1] == "ytqjk-knowledge@ytqjk" and "remove" in command:
            raise RuntimeError("recorded CLI failure")
        if "marketplace" in command:
            self.marketplaces.add("ytqjk")
        else:
            name = command[-1].split("@", maxsplit=1)[0]
            if "remove" in command:
                self.plugins.discard(name)
            else:
                self.plugins.add(name)
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
            self.assertFalse((target / ".ytqjk-uninstall").exists())

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

    def test_uninstall_removes_only_manifest_owned_stable_plugins(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "target"
            codex_root = Path(directory) / "codex"
            materialize_plugins(codex_root)
            unrelated = codex_root / "plugins" / "other-plugin"
            unrelated.mkdir(parents=True)
            runner = StatefulRunner()

            result = apply_uninstall_plan(
                build_uninstall_plan("all", target), target, runner,
                codex_root=codex_root,
            )

            self.assertIn(
                "plugins/ytqjk-agentic-orchestrator", result["removed_paths"]
            )
            self.assertFalse(
                (codex_root / "plugins" / "ytqjk-knowledge").exists()
            )
            self.assertTrue(unrelated.is_dir())
            self.assertFalse(
                (codex_root / "plugins" / ".ytqjk-managed.json").exists()
            )

    def test_uninstall_preserves_unmanaged_matching_directory(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "target"
            codex_root = Path(directory) / "codex"
            unmanaged = (
                codex_root / "plugins" / "ytqjk-agentic-orchestrator"
            )
            unmanaged.mkdir(parents=True)

            apply_uninstall_plan(
                build_uninstall_plan("codex-only", target), target,
                StatefulRunner(), codex_root=codex_root,
            )

            self.assertTrue(unmanaged.is_dir())

    def test_uninstall_rejects_modified_managed_stable_plugin(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "target"
            codex_root = Path(directory) / "codex"
            materialize_plugins(codex_root)
            marker = (
                codex_root / "plugins" / "ytqjk-knowledge" / "marker.txt"
            )
            marker.write_text("modified", encoding="utf-8")
            runner = StatefulRunner()

            with self.assertRaises(InstallError) as raised:
                apply_uninstall_plan(
                    build_uninstall_plan("all", target), target, runner,
                    codex_root=codex_root,
                )

            self.assertEqual(raised.exception.failed_action, "local-preflight")
            self.assertEqual(runner.commands, [])
            self.assertTrue(marker.is_file())

    def test_local_removal_failure_prevents_cli_mutation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            skill = target / "skills" / "ytqjk" / "SKILL.md"
            skill.parent.mkdir(parents=True)
            skill.write_text("owned", encoding="utf-8")
            runner = StatefulRunner()
            with mock.patch.object(
                uninstall_core.os, "replace", side_effect=OSError("failure")
            ), self.assertRaises(InstallError):
                apply_uninstall_plan(
                    build_uninstall_plan("all", target), target, runner
                )

            self.assertTrue(skill.is_file())
            self.assertEqual(runner.commands, [])
            self.assertFalse((target / ".ytqjk-uninstall").exists())

    def test_cli_failure_restores_local_paths_and_compensates(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "target"
            skill = target / "skills" / "ytqjk" / "SKILL.md"
            skill.parent.mkdir(parents=True)
            skill.write_text("owned", encoding="utf-8")
            codex_root = Path(directory) / "codex"
            materialize_plugins(codex_root)
            runner = FailingRemovalRunner()

            with self.assertRaises(InstallError) as raised:
                apply_uninstall_plan(
                    build_uninstall_plan("all", target), target, runner,
                    codex_root=codex_root,
                )

            self.assertEqual(raised.exception.rollback, "SUCCEEDED")
            self.assertTrue(skill.is_file())
            self.assertTrue(
                (codex_root / "plugins" / "ytqjk-knowledge").is_dir()
            )
            self.assertFalse((target / ".ytqjk-uninstall").exists())
            self.assertEqual(
                runner.plugins,
                {"ytqjk-agentic-orchestrator", "ytqjk-knowledge"},
            )
