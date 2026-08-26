from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
import zipfile
from http import HTTPStatus
from pathlib import Path
from threading import Lock
from unittest import mock


DASHBOARD = Path(__file__).resolve().parents[2] / "dashboard"
sys.path.insert(0, str(DASHBOARD))

import dashboard_update as update  # noqa: E402
import dashboard_update_http as update_http  # noqa: E402
import knowledge_dashboard as dashboard  # noqa: E402


class DashboardUpdateTest(unittest.TestCase):
    def test_check_reports_only_newer_stable_semver(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            plugin = self.plugin_root(Path(temporary), "0.3.2")
            release = self.release("0.4.0")

            result = update.check_update(plugin, loader=lambda: release)

        self.assertEqual(result["current_version"], "0.3.2")
        self.assertEqual(result["latest_version"], "0.4.0")
        self.assertTrue(result["update_available"])
        self.assertNotIn("archive_url", result)

    def test_release_rejects_prerelease_and_untrusted_urls(self) -> None:
        payload = self.release_payload("0.4.0")
        payload["prerelease"] = True
        with self.assertRaisesRegex(update.UpdateError, "正式发布"):
            update._release(payload)

        payload = self.release_payload("0.4.0")
        payload["zipball_url"] = "https://example.test/release.zip"
        with self.assertRaisesRegex(update.UpdateError, "地址无效"):
            update._release(payload)

        payload = self.release_payload("0.4.0")
        payload["zipball_url"] = "https://api.github.com/repos/other/repo/zipball/v0.4.0"
        with self.assertRaisesRegex(update.UpdateError, "地址无效"):
            update._release(payload)

        payload = self.release_payload("0.4.0")
        payload["tag_name"] = "v0.04.0"
        with self.assertRaisesRegex(update.UpdateError, "纯 SemVer"):
            update._release(payload)

        payload = self.release_payload("0.6.2.1")
        with self.assertRaisesRegex(update.UpdateError, "纯 SemVer"):
            update._release(payload)

    def test_extract_rejects_parent_escape(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "release.zip"
            with zipfile.ZipFile(archive, "w") as package:
                package.writestr("../escape.txt", "unsafe")

            with self.assertRaisesRegex(update.UpdateError, "不安全路径"):
                update.extract_release(archive, root / "source", "0.4.0")

            self.assertFalse((root / "escape.txt").exists())

    def test_extract_rejects_windows_drive_like_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            archive = root / "release.zip"
            with zipfile.ZipFile(archive, "w") as package:
                package.writestr("C:/setup.py", "unsafe")

            with self.assertRaisesRegex(update.UpdateError, "顶层目录"):
                update.extract_release(archive, root / "source", "0.4.0")

    def test_update_uses_managed_or_dashboard_bundle_codex_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            release = self.release("0.4.0")
            calls: list[tuple[Path, Path, str]] = []

            def download(_release: update.Release, destination: Path) -> None:
                self.write_release(destination, "0.4.0")

            def install(
                source: Path, codex_root: Path, version: str
            ) -> dict[str, object]:
                calls.append((source, codex_root, version))
                self.assertTrue((source / "setup.py").is_file())
                return {"apply": {"status": "APPLIED"}}

            cases = (
                (self.plugin_root(root / "stable", "0.3.2"), root / "stable"),
                (self.bundle_root(root / "bundle", "0.3.2"), root / "bundle"),
            )
            for plugin, expected_root in cases:
                with self.subTest(plugin=plugin):
                    calls.clear()
                    result = update.perform_update(
                        plugin,
                        loader=lambda: release,
                        downloader=download,
                        installer=install,
                    )
                    self.assertEqual(result["status"], "UPDATED")
                    self.assertEqual(result["latest_version"], "0.4.0")
                    self.assertTrue(result["restart_required"])
                    self.assertEqual(calls[0][1:], (expected_root, "0.4.0"))

    def test_update_rejects_release_manifest_version_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            plugin = self.plugin_root(root, "0.3.2")

            def download(_release: update.Release, destination: Path) -> None:
                self.write_release(destination, "0.3.9")

            with self.assertRaisesRegex(update.UpdateError, "版本不一致"):
                update.perform_update(
                    plugin,
                    loader=lambda: self.release("0.4.0"),
                    downloader=download,
                    installer=lambda *_: self.fail("installer must not run"),
                )

    def test_http_update_requires_same_origin_token(self) -> None:
        handler = _FakeHandler({"token": "wrong"})

        with mock.patch.object(update, "perform_update") as perform:
            update_http.handle_update_request(handler)

        perform.assert_not_called()
        self.assertEqual(handler.responses[0][1], HTTPStatus.BAD_REQUEST)
        self.assertEqual(
            handler.responses[0][0]["error_code"],
            "UPDATE_TOKEN_INVALID",
        )

    def test_http_status_keeps_local_version_when_release_check_fails(
        self,
    ) -> None:
        handler = _FakeHandler({})

        with mock.patch.object(
            update_http, "current_version", return_value="0.4.6"
        ), mock.patch.object(
            update_http,
            "check_update",
            side_effect=update.UpdateError("无法读取 GitHub 最新版本。"),
        ):
            update_http.send_update_status(handler)

        payload, status = handler.responses[0]
        self.assertEqual(status, HTTPStatus.BAD_GATEWAY)
        self.assertEqual(payload["current_version"], "0.4.6")
        self.assertEqual(payload["token"], "secret")

    def test_http_update_serializes_successful_install(self) -> None:
        handler = _FakeHandler({"token": "secret"})
        installed = {
            "status": "UPDATED",
            "latest_version": "0.4.0",
            "restart_required": True,
        }

        with mock.patch.object(
            update_http, "perform_update", return_value=installed
        ):
            update_http.handle_update_request(handler)

        self.assertEqual(handler.responses[0][0], {"ok": True, **installed})
        self.assertEqual(handler.events, ["response"])
        self.assertTrue(handler.restart_after_response)
        self.assertFalse(handler.update_lock.locked())

    def test_handler_schedules_restart_only_after_response_finishes(
        self,
    ) -> None:
        events: list[str] = []
        handler = object.__new__(dashboard.KnowledgeHandler)
        handler.restart_after_response = True
        handler.schedule_restart = lambda: events.append("restart")

        with mock.patch.object(
            dashboard.SimpleHTTPRequestHandler,
            "finish",
            side_effect=lambda: events.append("finish"),
        ):
            handler.finish()

        self.assertEqual(events, ["finish", "restart"])

    def test_installer_defers_dashboard_restart_to_release_setup(self) -> None:
        completed = subprocess.CompletedProcess(
            [],
            0,
            stdout=(
                '{"version":"0.4.9","apply":{"status":"APPLIED"}}'
            ),
            stderr="",
        )

        with mock.patch.object(
            update.subprocess, "run", return_value=completed
        ) as run:
            update.run_installer(Path("source"), Path("codex"), "0.4.9")

        command = run.call_args.args[0]
        mode = command.index("--mode")
        self.assertEqual(command[mode + 1], "codex-stable-only")
        self.assertNotIn("--dashboard-service", command)

    def test_windows_installer_process_is_hidden(self) -> None:
        completed = subprocess.CompletedProcess(
            [],
            0,
            stdout=(
                '{"version":"0.4.9","apply":{"status":"APPLIED"}}'
            ),
            stderr="",
        )

        with (
            mock.patch.object(update.sys, "platform", "win32"),
            mock.patch.object(
                update.subprocess, "run", return_value=completed
            ) as run,
        ):
            update.run_installer(Path("source"), Path("codex"), "0.4.9")

        self.assertEqual(
            run.call_args.kwargs["creationflags"], update.CREATE_NO_WINDOW
        )

    def test_installer_failure_uses_sanitized_stderr_receipt(self) -> None:
        completed = subprocess.CompletedProcess(
            [],
            2,
            stdout="",
            stderr=(
                '{"error":"private path","failed_action":'
                '"marketplace:ytqjk"}'
            ),
        )

        with mock.patch.object(update.subprocess, "run", return_value=completed):
            with self.assertRaisesRegex(
                update.UpdateError, "失败步骤 marketplace:ytqjk"
            ) as raised:
                update.run_installer(Path("source"), Path("codex"), "0.4.2")

        self.assertNotIn("private path", str(raised.exception))

    @staticmethod
    def plugin_root(root: Path, version: str) -> Path:
        plugins = root / "plugins"
        plugin = plugins / "ytqjk-agentic-orchestrator"
        manifest = plugin / ".codex-plugin" / "plugin.json"
        manifest.parent.mkdir(parents=True)
        manifest.write_text(
            json.dumps({
                "name": "ytqjk-agentic-orchestrator",
                "version": version,
            }),
            encoding="utf-8",
        )
        (plugins / ".ytqjk-managed.json").write_text("{}", encoding="utf-8")
        return plugin

    @staticmethod
    def bundle_root(root: Path, version: str) -> Path:
        plugin = root / "data/ytqjk/dashboard-service" / version
        manifest = plugin / ".codex-plugin" / "plugin.json"
        manifest.parent.mkdir(parents=True)
        manifest.write_text(
            json.dumps({
                "name": "ytqjk-agentic-orchestrator",
                "version": version,
            }),
            encoding="utf-8",
        )
        return plugin

    @staticmethod
    def release(version: str) -> update.Release:
        return update.Release(
            version,
            f"v{version}",
            "https://api.github.com/repos/0tingqu0/ytqjk-marketplace/zipball/"
            f"v{version}",
            "https://github.com/0tingqu0/ytqjk-marketplace/releases/tag/"
            f"v{version}",
        )

    @staticmethod
    def release_payload(version: str) -> dict[str, object]:
        release = DashboardUpdateTest.release(version)
        return {
            "draft": False,
            "prerelease": False,
            "tag_name": release.tag,
            "zipball_url": release.archive_url,
            "html_url": release.page_url,
        }

    @staticmethod
    def write_release(destination: Path, version: str) -> None:
        root = "0tingqu0-ytqjk-marketplace-fixture"
        with zipfile.ZipFile(destination, "w") as package:
            package.writestr(f"{root}/setup.py", "print('fixture')")
            for name in update.PLUGIN_NAMES:
                manifest = json.dumps({"name": name, "version": version})
                package.writestr(
                    f"{root}/plugins/{name}/.codex-plugin/plugin.json",
                    manifest,
                )


class _FakeHandler:
    def __init__(self, payload: dict[str, object]) -> None:
        self.payload = payload
        self.plugin_root = Path("managed-plugin")
        self.update_token = "secret"
        self.update_lock = Lock()
        self.responses: list[tuple[dict[str, object], HTTPStatus]] = []
        self.events: list[str] = []
        self.restart_after_response = False

    def read_payload(self) -> dict[str, object]:
        return self.payload

    def send_json(
        self,
        value: dict[str, object],
        status: HTTPStatus = HTTPStatus.OK,
    ) -> None:
        self.responses.append((value, status))
        self.events.append("response")

    def schedule_restart(self) -> None:
        self.events.append("restart")


if __name__ == "__main__":
    unittest.main()
