from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from platform_paths import default_knowledge_root, runtime_python  # noqa: E402


class PlatformPathsTest(unittest.TestCase):
    def test_environment_override_has_priority(self) -> None:
        configured = Path("自定义 资料")
        environment = {
            "YTQJK_KNOWLEDGE_ROOT": str(configured),
            "XDG_DATA_HOME": "/ignored",
            "LOCALAPPDATA": "C:/ignored",
        }
        self.assertEqual(
            default_knowledge_root(
                environ=environment,
                platform_name="linux",
                home=Path("/home/test"),
            ),
            configured,
        )

    def test_windows_prefers_d_drive_then_local_app_data(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            drive = base / "D"
            drive.mkdir()
            self.assertEqual(
                default_knowledge_root(
                    environ={},
                    platform_name="win32",
                    home=base / "home",
                    windows_root=drive,
                ),
                drive / "knowledge",
            )
            self.assertEqual(
                default_knowledge_root(
                    environ={"LOCALAPPDATA": str(base / "Local")},
                    platform_name="win32",
                    home=base / "home",
                    windows_root=base / "missing",
                ),
                base / "Local" / "YTQJK" / "knowledge",
            )

    def test_linux_uses_xdg_or_home_without_windows_literal(self) -> None:
        xdg = default_knowledge_root(
            environ={"XDG_DATA_HOME": "/data/shared"},
            platform_name="linux",
            home=Path("/home/test"),
        )
        fallback = default_knowledge_root(
            environ={}, platform_name="linux", home=Path("/home/test")
        )
        self.assertEqual(xdg, Path("/data/shared/ytqjk"))
        self.assertEqual(fallback, Path("/home/test/.local/share/ytqjk"))
        self.assertNotIn(r"D:\knowledge", str(fallback))

    def test_linux_ignores_relative_xdg_data_home(self) -> None:
        root = default_knowledge_root(
            environ={"XDG_DATA_HOME": "relative/data"},
            platform_name="linux",
            home=Path("/home/test"),
        )
        self.assertEqual(root, Path("/home/test/.local/share/ytqjk"))

    def test_runtime_python_is_platform_specific(self) -> None:
        runtime = Path("root") / ".runtime"
        self.assertEqual(
            runtime_python(runtime, "win32"), runtime / "Scripts" / "python.exe"
        )
        self.assertEqual(runtime_python(runtime, "linux"), runtime / "bin" / "python")

    def test_cli_defaults_consume_environment_override(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "repo"
            knowledge = base / "知识 缓存"
            repo.mkdir()
            subprocess.run(["git", "init", str(repo)], check=True, capture_output=True)
            environment = os.environ.copy()
            environment["YTQJK_KNOWLEDGE_ROOT"] = str(knowledge)

            rag = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPTS / "rag_cli.py"),
                    "init",
                    "--project-root",
                    str(repo),
                ],
                check=False,
                capture_output=True,
                encoding="utf-8",
                env=environment,
            )
            self.assertEqual(rag.returncode, 0, rag.stderr or rag.stdout)
            project_dir = Path(json.loads(rag.stdout)["project_dir"])
            self.assertEqual(project_dir.parents[1], knowledge.resolve())

            bootstrap = subprocess.run(
                [sys.executable, str(SCRIPTS / "bootstrap_runtime.py"), "--check"],
                check=False,
                capture_output=True,
                encoding="utf-8",
                env=environment,
            )
            self.assertEqual(
                bootstrap.returncode, 2, bootstrap.stderr or bootstrap.stdout
            )
            expected = runtime_python(knowledge.resolve() / ".runtime")
            self.assertEqual(Path(json.loads(bootstrap.stdout)["python"]), expected)
            self.assertFalse((knowledge / ".runtime").exists())


if __name__ == "__main__":
    unittest.main()
