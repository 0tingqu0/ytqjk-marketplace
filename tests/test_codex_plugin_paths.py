from __future__ import annotations

import hashlib
import io
import json
import tempfile
import unittest
from contextlib import nullcontext, redirect_stdout
from pathlib import Path
from unittest import mock

import codex_plugin_paths
from codex_plugin_paths import PLUGIN_NAMES, PluginPathError, manifest_path
from install_core import (
    InstallError, apply_plan, build_plan, normalize_update_mode,
)
from install_external_codex import materialize_plugins
from setup import main
from tests.test_install_external import StatefulRunner
from uninstall_core import apply_uninstall_plan, build_uninstall_plan


def digest_tree(root: Path) -> dict[str, str]:
    return {
        item.relative_to(root).as_posix(): hashlib.sha256(
            item.read_bytes()
        ).hexdigest()
        for item in root.rglob("*")
        if item.is_file()
    }


class StablePluginPathTest(unittest.TestCase):
    def test_stable_only_materializes_without_external_commands(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "target"
            codex_root = Path(directory) / "codex"
            plan = build_plan("codex-stable-only", target)

            result = apply_plan(plan, target, codex_root=codex_root)

            self.assertEqual(plan.actions, ())
            self.assertEqual(result["external_commands"], [])
            self.assertEqual(
                result["codex_plugins"]["stable_paths"],
                [f"plugins/{name}" for name in PLUGIN_NAMES],
            )

    def test_legacy_web_update_maps_to_stable_only(self) -> None:
        with tempfile.TemporaryDirectory(
            prefix="ytqjk-update-test-"
        ) as directory:
            mode = normalize_update_mode(
                "codex-only", Path(directory) / "source",
                "off", "off", "off",
            )

        self.assertEqual(mode, "codex-stable-only")

    def test_materializes_fixed_paths_with_relative_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            codex_root = Path(directory) / "codex"
            result = materialize_plugins(codex_root)
            self.assertTrue(result["changed"])
            for name in PLUGIN_NAMES:
                self.assertTrue((codex_root / "plugins" / name).is_dir())
            manifest = json.loads(manifest_path(codex_root).read_text())
            self.assertEqual(manifest["schema"], "ytqjk-managed-plugins/v1")
            self.assertNotIn(str(codex_root), json.dumps(manifest))
            repeated = materialize_plugins(codex_root)
            self.assertFalse(repeated["changed"])

    def test_rejects_non_managed_fixed_directory(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            codex_root = Path(directory) / "codex"
            conflict = codex_root / "plugins" / PLUGIN_NAMES[0]
            conflict.mkdir(parents=True)
            with self.assertRaisesRegex(PluginPathError, "not managed"):
                materialize_plugins(codex_root)

    def test_modified_managed_tree_rejects_upgrade(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            codex_root = Path(directory) / "codex"
            materialize_plugins(codex_root)
            marker = codex_root / "plugins" / PLUGIN_NAMES[0] / "marker.txt"
            marker.write_text("old", encoding="utf-8")
            before = digest_tree(codex_root / "plugins")

            with self.assertRaisesRegex(PluginPathError, "was modified"):
                materialize_plugins(codex_root)
            self.assertEqual(digest_tree(codex_root / "plugins"), before)

    def test_apply_compensates_codex_when_stable_path_conflicts(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "target"
            codex_root = Path(directory) / "codex"
            (codex_root / "plugins" / PLUGIN_NAMES[0]).mkdir(parents=True)
            runner = StatefulRunner()
            with self.assertRaises(InstallError) as raised:
                apply_plan(
                    build_plan("codex-only", target), target, runner=runner,
                    codex_root=codex_root,
                )
            self.assertEqual(raised.exception.rollback, "SUCCEEDED")
            self.assertFalse(runner.marketplaces or runner.plugins)

    def test_nested_link_rejects_upgrade_and_uninstall(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            codex_root = Path(directory) / "codex"
            materialize_plugins(codex_root)
            linked = (
                codex_root / "plugins" / PLUGIN_NAMES[0] / "skills"
                / "ytqjk" / "SKILL.md"
            )
            outside = Path(directory) / "outside.md"
            outside.write_bytes(linked.read_bytes())
            linked.unlink()
            try:
                linked.symlink_to(outside)
                detector = None
            except OSError:
                linked.write_bytes(outside.read_bytes())
                original = codex_plugin_paths._link_or_reparse
                detector = mock.patch(
                    "codex_plugin_paths._link_or_reparse",
                    side_effect=lambda path: path == linked or original(path),
                )

            with detector or nullcontext():
                with self.assertRaisesRegex(PluginPathError, "link or reparse"):
                    materialize_plugins(codex_root)
                runner = StatefulRunner()
                with self.assertRaises(InstallError):
                    apply_uninstall_plan(
                        build_uninstall_plan("codex-only", Path(directory)),
                        Path(directory), runner, codex_root=codex_root,
                    )
                self.assertEqual(runner.calls, [])


class StablePluginPathReceiptTest(unittest.TestCase):

    def test_cli_can_skip_dashboard_service_during_web_update(self) -> None:
        with tempfile.TemporaryDirectory(
            prefix="ytqjk-update-test-"
        ) as directory:
            target = Path(directory) / "source" / "release"
            codex_root = Path(directory) / "codex"
            output = io.StringIO()
            with (
                mock.patch("setup.configure_dashboard") as dashboard,
                mock.patch("setup.run_external") as external,
                redirect_stdout(output),
            ):
                code = main(
                    [
                        "--apply", "--yes", "--json",
                        "--mode", "codex-only",
                        "--target-root", str(target),
                        "--codex-root", str(codex_root),
                        "--codex-import", "off",
                        "--project-bootstrap", "off",
                        "--dashboard-service", "off",
                    ],
                )

            receipt = json.loads(output.getvalue())
            self.assertEqual(code, 0)
            dashboard.assert_not_called()
            external.assert_not_called()
            self.assertEqual(receipt["mode"], "codex-stable-only")
            self.assertEqual(receipt["apply"]["external_commands"], [])
            self.assertEqual(
                receipt["dashboard_service"]["status"], "SKIPPED_UPDATE"
            )

    def test_web_update_defers_dashboard_restart(self) -> None:
        with tempfile.TemporaryDirectory(prefix="ytqjk-update-test-") as root:
            target = Path(root) / "source" / "release"
            for mode in ("codex-only", "codex-stable-only"):
                output = io.StringIO()
                scheduled = {
                    "status": "RESTART_SCHEDULED",
                    "port": 8765,
                    "changed": False,
                }
                with (
                    self.subTest(mode=mode),
                    mock.patch("setup.configure_dashboard") as dashboard,
                    mock.patch(
                        "setup.schedule_dashboard_restart",
                        return_value=scheduled,
                    ) as restart,
                    redirect_stdout(output),
                ):
                    code = main(
                        [
                            "--apply", "--yes", "--json", "--mode", mode,
                            "--target-root", str(target),
                            "--codex-root", str(Path(root) / "codex"),
                            "--codex-import", "off",
                            "--project-bootstrap", "off",
                        ],
                        runner=StatefulRunner(),
                    )

                self.assertEqual(code, 0)
                dashboard.assert_not_called()
                restart.assert_called_once()
                self.assertEqual(
                    json.loads(output.getvalue())["dashboard_service"]["status"],
                    "RESTART_SCHEDULED",
                )

    def test_apply_records_stable_paths(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "target"
            codex_root = Path(directory) / "codex"
            result = apply_plan(
                build_plan("codex-only", target), target,
                runner=StatefulRunner(), codex_root=codex_root,
            )
            self.assertEqual(
                result["codex_plugins"]["stable_paths"],
                [f"plugins/{name}" for name in PLUGIN_NAMES],
            )

    def test_cli_uses_configured_codex_root_for_stable_paths(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "target"
            codex_root = Path(directory) / "codex"
            output = io.StringIO()
            dashboard = {
                "status": "RUNNING",
                "port": 8765,
                "autostart": "INSTALLED",
                "changed": True,
            }
            with (
                mock.patch("setup.configure_dashboard", return_value=dashboard),
                redirect_stdout(output),
            ):
                code = main(
                    [
                        "--apply", "--yes", "--json", "--mode", "codex-only",
                        "--target-root", str(target), "--codex-root", str(codex_root),
                    ],
                    runner=StatefulRunner(),
                )
            receipt = json.loads(output.getvalue())
            self.assertEqual(code, 0)
            self.assertTrue(
                (codex_root / "plugins" / PLUGIN_NAMES[1]).is_dir()
            )
            self.assertEqual(
                receipt["apply"]["codex_plugins"]["stable_paths"],
                [f"plugins/{name}" for name in PLUGIN_NAMES],
            )
