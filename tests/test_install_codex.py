from __future__ import annotations

import hashlib
import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import install_core
from install_core import InstallError, apply_plan, build_plan
from setup import main
from tests.test_install_external import StatefulRunner


def digest_tree(root: Path) -> dict[str, str]:
    return {
        item.relative_to(root).as_posix(): hashlib.sha256(
            item.read_bytes()
        ).hexdigest()
        for item in root.rglob("*")
        if item.is_file()
    }


def business_tree(root: Path) -> dict[str, str]:
    return {
        path: digest
        for path, digest in digest_tree(root).items()
        if not path.startswith(".ytqjk-install/")
    }


class CodexInstallTest(unittest.TestCase):
    def test_clean_all_installs_staged_grill_and_codex(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            runner = StatefulRunner()
            plan = build_plan("all", target)
            result = apply_plan(plan, target, runner=runner)
            self.assertTrue(
                (target / "skills" / "grill-me" / "SKILL.md").is_file()
            )
            self.assertTrue(result["changed"])
            self.assertEqual(runner.marketplaces, {"ytqjk"})
            self.assertEqual(
                runner.plugins,
                {"ytqjk-agentic-orchestrator", "ytqjk-knowledge"},
            )
            npx_call = next(
                call for call in runner.calls if call[0][0] == "npx"
            )
            self.assertTrue(npx_call[1].is_relative_to(target.resolve()))

    def test_failed_apply_reports_cleanup_failure_separately(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            marker = target / "README"
            marker.write_text("old", encoding="utf-8")
            before = business_tree(target)
            runner = StatefulRunner(fail_mutation=1)
            cleanup = mock.patch.object(
                install_core,
                "cleanup_stage",
                side_effect=OSError("cleanup failed"),
            )
            with cleanup, self.assertRaises(InstallError) as raised:
                apply_plan(build_plan("all", target), target, runner=runner)
            error = raised.exception
            self.assertEqual(error.rollback, "SUCCEEDED")
            self.assertTrue(error.staging_residue)
            self.assertEqual(
                error.cleanup_action,
                "remove-target-root-staging-residue",
            )
            self.assertEqual(business_tree(target), before)
            residue = target / ".ytqjk-install" / "staging"
            self.assertTrue(residue.is_dir())
            self.assertFalse(runner.marketplaces or runner.plugins)

    def test_cli_receipts_distinguish_apply_and_cleanup(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            cleanup = mock.patch.object(
                install_core,
                "cleanup_stage",
                side_effect=OSError("cleanup failed"),
            )
            output = io.StringIO()
            with cleanup, redirect_stdout(output):
                code = main(
                    [
                        "--apply", "--yes", "--json",
                        "--mode", "ide-only",
                        "--target-root", str(target),
                    ],
                    runner=StatefulRunner(),
                )
            receipt = json.loads(output.getvalue())
            self.assertEqual(code, 0)
            self.assertEqual(receipt["apply"]["status"], "APPLIED")
            self.assertEqual(receipt["apply"]["cleanup"], "FAILED")

            error = io.StringIO()
            runner = StatefulRunner(fail_mutation=1)
            with cleanup, redirect_stderr(error):
                code = main(
                    [
                        "--apply", "--yes", "--json",
                        "--mode", "all",
                        "--target-root", str(target / "failed"),
                    ],
                    runner=runner,
                )
            receipt = json.loads(error.getvalue())
            self.assertEqual(code, 2)
            self.assertEqual(receipt["rollback"], "SUCCEEDED")
            self.assertEqual(receipt["cleanup"], "FAILED")

    def test_codex_failure_after_npx_restores_target_and_state(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            marker = target / "README"
            marker.write_text("old", encoding="utf-8")
            before = digest_tree(target)
            runner = StatefulRunner(fail_mutation=2)
            with self.assertRaises(InstallError) as raised:
                apply_plan(build_plan("all", target), target, runner=runner)
            self.assertEqual(raised.exception.rollback, "SUCCEEDED")
            self.assertEqual(digest_tree(target), before)
            self.assertFalse(runner.marketplaces or runner.plugins)

    def test_compensation_failure_is_reported(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            runner = StatefulRunner(
                fail_mutation=2, fail_compensation=True
            )
            with self.assertRaises(InstallError) as raised:
                apply_plan(
                    build_plan("codex-only", target), target, runner=runner
                )
            self.assertEqual(raised.exception.rollback, "FAILED")
            self.assertEqual(runner.marketplaces, {"ytqjk"})
