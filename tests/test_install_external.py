from __future__ import annotations

import hashlib
import io
import json
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest import mock

import install_core
import install_external
from install_core import InstallError, apply_plan, build_plan
from setup import main, run_external


def digest_tree(root: Path) -> dict[str, str]:
    return {
        item.relative_to(root).as_posix(): hashlib.sha256(
            item.read_bytes()
        ).hexdigest()
        for item in root.rglob("*")
        if item.is_file()
    }


class StatefulRunner:
    def __init__(
        self,
        marketplaces: set[str] | None = None,
        plugins: set[str] | None = None,
        fail_mutation: int | None = None,
        fail_npx: bool = False,
        mutate_target_on_npx_failure: bool = False,
        fail_compensation: bool = False,
        escape: bool = False,
    ) -> None:
        self.marketplaces = set(marketplaces or ())
        self.plugins = set(plugins or ())
        self.fail_mutation = fail_mutation
        self.fail_npx = fail_npx
        self.mutate_target_on_npx_failure = mutate_target_on_npx_failure
        self.fail_compensation = fail_compensation
        self.escape = escape
        self.mutations = 0
        self.calls: list[tuple[list[str], Path]] = []

    def __call__(self, command: list[str], cwd: Path) -> str:
        cwd = cwd.resolve()
        self.calls.append((command, cwd))
        if command[0] == "npx":
            if self.fail_npx:
                if self.mutate_target_on_npx_failure:
                    target = cwd.parents[3]
                    changed = target / "skills" / "injected" / "SKILL.md"
                    changed.parent.mkdir(parents=True, exist_ok=True)
                    changed.write_text("partial", encoding="utf-8")
                raise RuntimeError("recorded npx failure")
            skill = cwd / "skills" / "grill-me"
            skill.mkdir(parents=True)
            (skill / "SKILL.md").write_text("staged", encoding="utf-8")
            if self.escape:
                link = cwd / "skills" / "escape"
                link.symlink_to(cwd.parent.parent, target_is_directory=True)
            return ""
        if command[-2:] == ["list", "--json"]:
            values = (
                self.marketplaces
                if "marketplace" in command
                else self.plugins
            )
            return json.dumps([{"name": value} for value in values])
        self.mutations += 1
        if self.fail_mutation == self.mutations:
            raise RuntimeError("recorded Codex failure")
        removing = "remove" in command
        if removing and self.fail_compensation:
            raise RuntimeError("recorded compensation failure")
        if "marketplace" in command:
            self._change(self.marketplaces, "ytqjk", removing)
        else:
            identity = command[-1].split("@", maxsplit=1)[0]
            self._change(self.plugins, identity, removing)
        return ""

    @staticmethod
    def _change(values: set[str], identity: str, removing: bool) -> None:
        if removing:
            values.discard(identity)
        else:
            values.add(identity)


class ExternalInstallTest(unittest.TestCase):
    def test_clean_ide_installs_staged_grill(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            runner = StatefulRunner()
            apply_plan(build_plan("ide-only", target), target, runner=runner)
            skill = target / "skills" / "grill-me" / "SKILL.md"
            self.assertTrue(skill.is_file())
            has_codex = any(call[0][0] == "codex" for call in runner.calls)
            self.assertFalse(has_codex)

    def test_preexisting_grill_skips_npx_and_preserves_hash(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            skill = target / "grill-me" / "SKILL.md"
            skill.parent.mkdir(parents=True)
            skill.write_text("existing", encoding="utf-8")
            before = hashlib.sha256(skill.read_bytes()).hexdigest()
            runner = StatefulRunner()
            apply_plan(build_plan("ide-only", target), target, runner=runner)
            after = hashlib.sha256(skill.read_bytes()).hexdigest()
            self.assertEqual(after, before)
            self.assertFalse(any(call[0][0] == "npx" for call in runner.calls))

    def test_npx_failure_restores_target(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            marker = target / "README"
            marker.write_text("old", encoding="utf-8")
            before = digest_tree(target)
            runner = StatefulRunner(fail_npx=True)
            with self.assertRaises(InstallError):
                plan = build_plan("ide-only", target)
                apply_plan(plan, target, runner=runner)
            self.assertEqual(digest_tree(target), before)

    def test_npx_mutation_then_failure_restores_target(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            old = target / "skills" / "old" / "README"
            old.parent.mkdir(parents=True)
            old.write_text("old", encoding="utf-8")
            before = digest_tree(target)
            runner = StatefulRunner(
                fail_npx=True,
                mutate_target_on_npx_failure=True,
            )
            with self.assertRaises(InstallError) as raised:
                apply_plan(
                    build_plan("ide-only", target), target, runner=runner
                )
            self.assertEqual(raised.exception.rollback, "SUCCEEDED")
            self.assertEqual(digest_tree(target), before)

    def test_backup_failure_cleans_staging_and_reports_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            old = target / "skills" / "old" / "README"
            old.parent.mkdir(parents=True)
            old.write_text("old", encoding="utf-8")
            before = digest_tree(target / "skills")
            failure = OSError("recorded backup failure")
            backup = mock.patch.object(
                install_external.shutil,
                "copytree",
                side_effect=failure,
            )
            error = io.StringIO()
            with backup, redirect_stderr(error):
                code = main(
                    [
                        "--apply", "--yes", "--json",
                        "--mode", "ide-only",
                        "--target-root", str(target),
                    ],
                    runner=StatefulRunner(),
                )
            receipt = json.loads(error.getvalue())
            self.assertEqual(code, 2)
            self.assertEqual(receipt["rollback"], "NOT_NEEDED")
            self.assertEqual(receipt["cleanup"], "SUCCEEDED")
            self.assertFalse(receipt["staging_residue"])
            self.assertEqual(digest_tree(target / "skills"), before)
            staging = target / ".ytqjk-install" / "staging"
            self.assertFalse(staging.exists())

    def test_partial_backup_cleanup_failure_reports_residue(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            old = target / "skills" / "old" / "README"
            old.parent.mkdir(parents=True)
            old.write_text("old", encoding="utf-8")
            before = digest_tree(target / "skills")

            failure = OSError("recorded partial backup failure")

            def fail_after_partial_copy(
                source: Path, destination: Path, **_: object
            ) -> None:
                destination.mkdir(parents=True)
                (destination / "partial").write_text(
                    "partial", encoding="utf-8"
                )
                raise failure

            backup = mock.patch.object(
                install_external.shutil,
                "copytree",
                side_effect=fail_after_partial_copy,
            )
            cleanup = mock.patch.object(
                install_external.shutil,
                "rmtree",
                side_effect=OSError("recorded cleanup failure"),
            )
            with backup, cleanup, self.assertRaises(InstallError) as raised:
                apply_plan(
                    build_plan("ide-only", target),
                    target,
                    runner=StatefulRunner(),
                )
            error = raised.exception
            self.assertEqual(error.rollback, "NOT_NEEDED")
            self.assertEqual(error.cleanup, "FAILED")
            self.assertTrue(error.staging_residue)
            self.assertEqual(
                error.cleanup_action,
                "remove-target-root-staging-residue",
            )
            self.assertIs(error.__cause__.__cause__, failure)
            self.assertEqual(digest_tree(target / "skills"), before)
            staging = target / ".ytqjk-install" / "staging"
            self.assertTrue(staging.is_dir())

    def test_production_npx_environment_is_staging_scoped(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            staging = Path(directory).resolve()
            completed = mock.Mock(stdout="")
            patched = mock.patch(
                "setup.subprocess.run", return_value=completed
            )
            with patched as run:
                run_external(["npx", "skills@latest", "add", "repo"], staging)
            kwargs = run.call_args.kwargs
            self.assertEqual(kwargs["cwd"], staging)
            self.assertFalse(kwargs["shell"])
            environment = kwargs["env"]
            for name in (
                "HOME",
                "USERPROFILE",
                "XDG_CACHE_HOME",
                "XDG_CONFIG_HOME",
                "npm_config_cache",
                "npm_config_prefix",
                "npm_config_userconfig",
            ):
                self.assertTrue(
                    Path(environment[name]).resolve().is_relative_to(staging)
                )

    def test_success_reports_applied_when_cleanup_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            runner = StatefulRunner()
            cleanup = mock.patch.object(
                install_core,
                "cleanup_stage",
                side_effect=OSError("cleanup failed"),
            )
            with cleanup:
                result = apply_plan(
                    build_plan("ide-only", target), target, runner=runner
                )
            self.assertEqual(result["status"], "APPLIED")
            self.assertTrue(result["staging_residue"])
            self.assertEqual(
                result["cleanup_action"],
                "remove-target-root-staging-residue",
            )
            grill = target / "skills" / "grill-me" / "SKILL.md"
            self.assertTrue(grill.is_file())

    def test_staging_escape_is_blocked_and_restored(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            before = digest_tree(target)
            runner = StatefulRunner(escape=True)
            with self.assertRaises(InstallError):
                plan = build_plan("ide-only", target)
                apply_plan(plan, target, runner=runner)
            self.assertEqual(digest_tree(target), before)
