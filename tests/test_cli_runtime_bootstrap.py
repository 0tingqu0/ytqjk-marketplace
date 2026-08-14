from __future__ import annotations

import hashlib
import io
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock
import zipfile

import bootstrap_cli_runtime
from bootstrap_cli_runtime import (
    CODEX_VERSION,
    NODE_ARCHIVE,
    NODE_VERSION,
    _extract_node_archive,
    default_runtime_root,
    ensure_cli_runtime,
)


def node_archive() -> bytes:
    output = io.BytesIO()
    root = NODE_ARCHIVE.removesuffix(".zip")
    with zipfile.ZipFile(output, "w") as archive:
        archive.writestr(f"{root}/node.exe", b"node")
        archive.writestr(f"{root}/npm.cmd", b"npm")
        archive.writestr(f"{root}/npx.cmd", b"npx")
    return output.getvalue()


class FakeNetwork:
    def __init__(self, archive: bytes, checksum: str | None = None) -> None:
        self.archive = archive
        self.checksum = checksum or hashlib.sha256(archive).hexdigest()
        self.calls: list[str] = []

    def __call__(self, url: str, destination: Path) -> None:
        self.calls.append(url)
        if url.endswith("SHASUMS256.txt"):
            payload = f"{self.checksum}  {NODE_ARCHIVE}\n".encode()
        else:
            payload = self.archive
        destination.write_bytes(payload)


class FakeExecutor:
    def __init__(self, fail_npm: bool = False) -> None:
        self.fail_npm = fail_npm
        self.calls: list[list[str]] = []

    def __call__(
        self, command: list[str], environment: dict[str, str]
    ) -> subprocess.CompletedProcess[str]:
        self.calls.append(command)
        executable = Path(command[0]).name.lower()
        if executable == "node.exe":
            return subprocess.CompletedProcess(command, 0, f"v{NODE_VERSION}\n", "")
        if executable == "codex.cmd":
            return subprocess.CompletedProcess(
                command, 0, f"codex-cli {CODEX_VERSION}\n", ""
            )
        if executable == "npm.cmd":
            if self.fail_npm:
                raise subprocess.CalledProcessError(1, command)
            prefix = Path(command[command.index("--prefix") + 1])
            prefix.mkdir(parents=True)
            (prefix / "codex.cmd").write_text("codex", encoding="ascii")
            return subprocess.CompletedProcess(command, 0, "", "")
        raise AssertionError(command)


class CliRuntimeBootstrapTest(unittest.TestCase):
    def test_default_root_is_scoped_to_local_app_data(self) -> None:
        local_app_data = r"C:\Users\tester\AppData\Local"
        root = default_runtime_root({"LOCALAPPDATA": local_app_data}, "Windows")
        self.assertEqual(root, Path(local_app_data) / "YTQJK" / "runtime")

    def test_missing_commands_install_verified_runtime(self) -> None:
        archive = node_archive()
        network = FakeNetwork(archive)
        executor = FakeExecutor()
        with tempfile.TemporaryDirectory() as directory:
            result = ensure_cli_runtime(
                {"codex", "npx"},
                runtime_root=Path(directory),
                system="Windows",
                which=lambda _: None,
                downloader=network,
                executor=executor,
            )

            self.assertTrue(
                {"codex", "node", "npm", "npx"}.issubset(result.executables)
            )
            self.assertTrue(Path(result.executables["codex"]).is_file())
            self.assertTrue(Path(result.executables["npx"]).is_file())
            self.assertEqual(result.status, "BOOTSTRAPPED")
        self.assertEqual(len(network.calls), 2)

    def test_inaccessible_path_command_is_bootstrapped(self) -> None:
        delegate = FakeExecutor()

        def executor(
            command: list[str], environment: dict[str, str]
        ) -> subprocess.CompletedProcess[str]:
            if "WindowsApps" in command[0]:
                raise PermissionError("command alias is inaccessible")
            return delegate(command, environment)

        with tempfile.TemporaryDirectory() as directory:
            result = ensure_cli_runtime(
                {"codex"},
                runtime_root=Path(directory),
                system="Windows",
                which=lambda _: r"C:\WindowsApps\codex.exe",
                downloader=FakeNetwork(node_archive()),
                executor=executor,
            )

        self.assertEqual(result.status, "BOOTSTRAPPED")
        self.assertEqual(result.provisioned, ("node", "codex"))

    def test_checksum_mismatch_stops_before_extraction(self) -> None:
        network = FakeNetwork(node_archive(), checksum="0" * 64)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaisesRegex(RuntimeError, "checksum"):
                ensure_cli_runtime(
                    {"npx"},
                    runtime_root=root,
                    system="Windows",
                    which=lambda _: None,
                    downloader=network,
                    executor=FakeExecutor(),
                )
            self.assertFalse(any(root.glob("node-*")))

    def test_archive_rejects_parent_escape_and_symlink(self) -> None:
        for member, mode in (
            ("../escape", 0),
            (f"{NODE_ARCHIVE.removesuffix('.zip')}/link", stat.S_IFLNK),
        ):
            with self.subTest(member=member):
                with tempfile.TemporaryDirectory() as directory:
                    archive_path = Path(directory) / NODE_ARCHIVE
                    info = zipfile.ZipInfo(member)
                    info.external_attr = (mode | 0o777) << 16
                    with zipfile.ZipFile(archive_path, "w") as archive:
                        archive.writestr(info, b"payload")
                    with self.assertRaisesRegex(RuntimeError, "unsafe"):
                        _extract_node_archive(
                            archive_path, Path(directory) / "output"
                        )
                    self.assertFalse((Path(directory) / "escape").exists())

    def test_failed_npm_install_does_not_publish_codex(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaisesRegex(RuntimeError, "Codex CLI"):
                ensure_cli_runtime(
                    {"codex"},
                    runtime_root=root,
                    system="Windows",
                    which=lambda _: None,
                    downloader=FakeNetwork(node_archive()),
                    executor=FakeExecutor(fail_npm=True),
                )
            self.assertFalse((root / f"codex-{CODEX_VERSION}").exists())

    def test_valid_runtime_is_reused_without_download(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ensure_cli_runtime(
                {"codex", "npx"},
                runtime_root=root,
                system="Windows",
                which=lambda _: None,
                downloader=FakeNetwork(node_archive()),
                executor=FakeExecutor(),
            )

            def unexpected_download(url: str, destination: Path) -> None:
                self.fail(f"unexpected download: {url}")

            repeated = ensure_cli_runtime(
                {"codex", "npx"},
                runtime_root=root,
                system="Windows",
                which=lambda _: None,
                downloader=unexpected_download,
                executor=FakeExecutor(),
            )
            self.assertEqual(repeated.status, "REUSED")

    def test_reuse_does_not_scan_unrelated_npm_cache(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ensure_cli_runtime(
                {"codex", "npx"}, runtime_root=root, system="Windows",
                which=lambda _: None, downloader=FakeNetwork(node_archive()),
                executor=FakeExecutor(),
            )
            cache_file = root / "npm-cache" / "volatile"
            cache_file.parent.mkdir()
            cache_file.write_text("cache", encoding="ascii")
            original = bootstrap_cli_runtime._is_reparse

            def inspect(path: Path) -> bool:
                if "npm-cache" in path.parts:
                    raise OSError("volatile cache entry")
                return original(path)

            with mock.patch(
                "bootstrap_cli_runtime._is_reparse", side_effect=inspect
            ):
                repeated = ensure_cli_runtime(
                    {"codex", "npx"}, runtime_root=root, system="Windows",
                    which=lambda _: None, downloader=lambda *_: self.fail(),
                    executor=FakeExecutor(),
                )
            self.assertEqual(repeated.status, "REUSED")


if __name__ == "__main__":
    unittest.main()
