from __future__ import annotations

import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import nullcontext
from pathlib import Path
from unittest import mock

import codex_plugin_paths
from codex_plugin_paths import PLUGIN_NAMES, PluginPathError, validate_targets
from install_external_codex import materialize_plugins
from tests.test_uninstall import StatefulRunner
from uninstall_core import apply_uninstall_plan, build_uninstall_plan


SOURCE_ROOT = Path(__file__).resolve().parents[1] / "plugins"


def generate_dashboard_cache(codex_root: Path) -> list[Path]:
    skill_root = (
        codex_root / "plugins" / "ytqjk-knowledge"
        / "skills" / "ytqjk-knowledge"
    )
    subprocess.run(
        [
            sys.executable,
            "-c",
            "import sys; sys.path.insert(0, sys.argv[1]); import workbench.app",
            str(skill_root),
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    return list(skill_root.rglob("__pycache__/*.pyc"))


class GeneratedPluginCacheTest(unittest.TestCase):
    def test_dashboard_cache_allows_validate_upgrade_and_uninstall(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            codex_root = root / "codex"
            source_root = root / "plugins"
            shutil.copytree(SOURCE_ROOT, source_root)
            materialize_plugins(codex_root, source_root)

            self.assertTrue(generate_dashboard_cache(codex_root))
            self.assertIsNotNone(validate_targets(codex_root))
            marker = (
                source_root / "ytqjk-knowledge" / "skills"
                / "ytqjk-knowledge" / "workbench" / "static" / "app.css"
            )
            marker.write_text(
                marker.read_text(encoding="utf-8") + "\n/* upgrade */\n",
                encoding="utf-8",
            )
            self.assertTrue(
                materialize_plugins(codex_root, source_root)["changed"]
            )

            self.assertTrue(generate_dashboard_cache(codex_root))
            result = apply_uninstall_plan(
                build_uninstall_plan("codex-only", root / "target"),
                root / "target",
                StatefulRunner(),
                codex_root=codex_root,
            )
            self.assertEqual(result["status"], "UNINSTALLED")
            for name in PLUGIN_NAMES:
                self.assertFalse((codex_root / "plugins" / name).exists())

    def test_generated_cache_rejects_unexpected_entries(self) -> None:
        cases = ("notes.txt", "nested")
        for name in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                codex_root = Path(directory) / "codex"
                materialize_plugins(codex_root)
                cache = (
                    codex_root / "plugins" / PLUGIN_NAMES[0] / "__pycache__"
                )
                cache.mkdir()
                entry = cache / name
                if name == "nested":
                    entry.mkdir()
                else:
                    entry.write_text("unexpected", encoding="utf-8")

                with self.assertRaisesRegex(PluginPathError, "generated cache"):
                    validate_targets(codex_root)

    def test_generated_cache_rejects_link_or_reparse_entry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            codex_root = Path(directory) / "codex"
            materialize_plugins(codex_root)
            cache = codex_root / "plugins" / PLUGIN_NAMES[0] / "__pycache__"
            cache.mkdir()
            entry = cache / "linked.pyc"
            outside = Path(directory) / "outside.pyc"
            outside.write_bytes(b"compiled")
            try:
                entry.symlink_to(outside)
                detector = nullcontext()
            except OSError:
                entry.write_bytes(outside.read_bytes())
                original = codex_plugin_paths._link_or_reparse
                detector = mock.patch(
                    "codex_plugin_paths._link_or_reparse",
                    side_effect=lambda path: path == entry or original(path),
                )

            with detector, self.assertRaisesRegex(
                PluginPathError, "link or reparse"
            ):
                validate_targets(codex_root)

    @unittest.skipUnless(hasattr(os, "mkfifo"), "FIFO is unavailable")
    def test_generated_cache_rejects_non_regular_entry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            codex_root = Path(directory) / "codex"
            materialize_plugins(codex_root)
            cache = codex_root / "plugins" / PLUGIN_NAMES[0] / "__pycache__"
            cache.mkdir()
            os.mkfifo(cache / "runtime.pyc")

            with self.assertRaisesRegex(PluginPathError, "generated cache"):
                validate_targets(codex_root)
