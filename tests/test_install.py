from __future__ import annotations

import json
import hashlib
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from install_core import (
    PUBLIC_MODES, VERSION, InstallError, Plan, apply_plan, build_plan,
    contained_path, target_has_grill_me,
)
from setup import main, vector_result


ROOT = Path(__file__).resolve().parents[1]
SETUP = ROOT / "setup.py"

def run(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [
            sys.executable, str(SETUP), *args,
            "--codex-import", "off", "--project-bootstrap", "off",
            "--json",
        ], text=True,
        capture_output=True, check=False, cwd=ROOT,
    )

def digest_tree(root: Path) -> dict[str, str]:
    files = [item for item in root.rglob("*") if item.is_file()]
    return {
        item.relative_to(root).as_posix(): hashlib.sha256(
            item.read_bytes()
        ).hexdigest()
        for item in files
    }


def add_grill(target: Path) -> None:
    skill = target / "grill-me" / "SKILL.md"
    skill.parent.mkdir(parents=True, exist_ok=True)
    skill.write_text("fixture", encoding="utf-8")


def file_plan(mode: str, target: Path) -> Plan:
    plan = build_plan(mode, target)
    return Plan(plan.mode, (), plan.files)


class InstallTest(unittest.TestCase):
    def test_dry_run_all_modes_and_schema(self) -> None:
        for mode in PUBLIC_MODES:
            result = run("--mode", mode)
            self.assertEqual(result.returncode, 0, result.stderr)
            data = json.loads(result.stdout)
            self.assertEqual((data["mode"], data["version"]), (mode, VERSION))
            self.assertEqual(data["schema"], "ytqjk-install-receipt/v1")
            self.assertTrue(data["dry_run"])
        version = subprocess.run(
            [sys.executable, str(SETUP), "--version"], text=True,
            capture_output=True, check=False, cwd=ROOT,
        )
        self.assertEqual(version.stdout.strip(), "0.4.2")

    def test_apply_needs_yes_and_target(self) -> None:
        self.assertNotEqual(run("--apply").returncode, 0)
        with tempfile.TemporaryDirectory() as directory:
            result = run(
                "--apply", "--yes", "--target-root", directory,
                "--mode", "knowledge-only",
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            skill = (
                Path(directory) / "skills" / "ytqjk-knowledge" / "SKILL.md"
            )
            self.assertTrue(skill.is_file())

    def test_idempotent_and_empty_snapshot(self) -> None:
        with tempfile.TemporaryDirectory(prefix="ytqjk space ") as directory:
            target = Path(directory)
            args = (
                "--apply", "--yes", "--target-root", str(target), "--mode",
                "knowledge-only",
            )
            self.assertEqual(run(*args).returncode, 0)
            repeated = run(*args)
            self.assertEqual(repeated.returncode, 0)
            apply = json.loads(repeated.stdout)["apply"]
            self.assertFalse(apply["changed"] or apply["snapshot"])

    def test_rollback_restores_full_multi_destination_trees(self) -> None:
        with tempfile.TemporaryDirectory(prefix="ytqjk space ") as directory:
            target = Path(directory)
            first = target / "skills" / "ytqjk"
            second = target / "skills" / "caveman"
            (first / "nested").mkdir(parents=True)
            second.mkdir(parents=True)
            (first / "README").write_text("only README", encoding="utf-8")
            nested = first / "nested" / "SKILL.md"
            nested.write_text("old ytqjk", encoding="utf-8")
            (second / "README").write_text("old caveman", encoding="utf-8")
            before = digest_tree(target)
            plan = file_plan("ide-only", target)
            with self.assertRaisesRegex(InstallError, "installation failed"):
                apply_plan(
                    plan, target, fail_on_copy=2,
                    runner=lambda command, cwd: "",
                )
            self.assertEqual(digest_tree(target), before)
            self.assertTrue((first / "README").is_file())
            self.assertTrue((second / "README").is_file())

    def test_relative_target_upgrade_rollback_and_space_path(self) -> None:
        temporary = tempfile.TemporaryDirectory(
            dir=ROOT, prefix="relative space "
        )
        with temporary as temp:
            absolute = Path(temp)
            target = absolute.relative_to(ROOT)
            plan = file_plan("ide-only", target)
            first = apply_plan(
                plan, target, runner=lambda command, cwd: ""
            )
            self.assertIsNone(first["snapshot"])
            skill = absolute / "skills" / "ytqjk" / "SKILL.md"
            skill.write_text("relative old", encoding="utf-8")
            caveman = absolute / "skills" / "caveman" / "SKILL.md"
            caveman.write_text("relative caveman old", encoding="utf-8")
            before = digest_tree(absolute)
            with self.assertRaisesRegex(InstallError, "installation failed"):
                apply_plan(plan, target, fail_on_copy=2)
            self.assertEqual(digest_tree(absolute), before)

    def test_path_containment_rejects_parent_and_symlink_escape(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory).resolve()
            with self.assertRaisesRegex(RuntimeError, "escapes"):
                contained_path(target, target / ".." / "outside")

    def test_grill_me_target_only_vector_and_plugin_plan(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            self.assertFalse(target_has_grill_me(target))
            grill = target / "local" / "grill-me" / "SKILL.md"
            grill.parent.mkdir(parents=True)
            grill.write_text("x", encoding="utf-8")
            self.assertTrue(target_has_grill_me(target))
        values = (vector_result("auto", 0, 0),
                  vector_result("auto", 0, 2000),
                  vector_result("on", 0, 0, True))
        self.assertEqual(values, ("lexical-only", "planned", "lexical-only"))
        self.assertEqual(len(build_plan("codex-only", None).actions), 3)

    def test_dry_run_never_calls_runner(self) -> None:
        recorded: list[list[str]] = []
        def record(command: list[str], cwd: Path) -> str:
            recorded.append(command)
            return ""

        code = main(["--mode", "all", "--json"], runner=record)
        self.assertEqual((code, recorded), (0, []))

    def test_faults_restore_hashes(self) -> None:
        for stage in ("before-mkdir", "before-copy"):
            with self.subTest(stage=stage):
                self._assert_fault_restores(stage)

    def test_new_destination_mkdir_fault_removes_created_tree(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)

            def inject(current: str, index: int, destination: Path) -> None:
                if current == "before-mkdir" and index == 1:
                    raise OSError("injected new mkdir")

            plan = file_plan("ide-only", target)
            with self.assertRaisesRegex(InstallError, "installation failed"):
                apply_plan(
                    plan, target, runner=lambda command, cwd: "", fault=inject
                )
            self.assertEqual(list(target.iterdir()), [])

    def _assert_fault_restores(self, stage: str) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            old = target / "skills" / "ytqjk"
            (old / "nested").mkdir(parents=True)
            (old / "README").write_text("readme", encoding="utf-8")
            nested = old / "nested" / "SKILL.md"
            nested.write_text("nested", encoding="utf-8")
            before = digest_tree(target)

            def inject(current: str, index: int, destination: Path) -> None:
                if current == stage and index == 1:
                    if current == "before-copy":
                        destination.mkdir(parents=True, exist_ok=True)
                        (destination / "partial").write_text("x")
                    raise OSError(f"injected {stage}")

            plan = file_plan("ide-only", target)
            with self.assertRaisesRegex(InstallError, "installation failed"):
                apply_plan(
                    plan, target, runner=lambda command, cwd: "", fault=inject
                )
            self.assertEqual(digest_tree(target), before)
