"""Secure GitHub Release checks and self-update orchestration."""
from __future__ import annotations

import json
import os
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import zipfile
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen


REPOSITORY = "0tingqu0/ytqjk-marketplace"
LATEST_RELEASE_URL = f"https://api.github.com/repos/{REPOSITORY}/releases/latest"
PLUGIN_NAMES = ("ytqjk-agentic-orchestrator", "ytqjk-knowledge")
MAX_METADATA_BYTES = 1024 * 1024
MAX_ARCHIVE_BYTES = 64 * 1024 * 1024
MAX_EXTRACTED_BYTES = 256 * 1024 * 1024
MAX_ARCHIVE_FILES = 10_000
CREATE_NO_WINDOW = getattr(subprocess, "CREATE_NO_WINDOW", 0x08000000)


class UpdateError(RuntimeError):
    """A sanitized update failure that is safe to show in the dashboard."""


@dataclass(frozen=True)
class Release:
    version: str
    tag: str
    archive_url: str
    page_url: str


def current_version(plugin_root: Path) -> str:
    """Read and validate the installed orchestrator version."""
    manifest = plugin_root / ".codex-plugin" / "plugin.json"
    try:
        data = json.loads(manifest.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise UpdateError("当前插件版本信息无效。") from error
    if data.get("name") != PLUGIN_NAMES[0]:
        raise UpdateError("当前插件身份无效。")
    return _version(data.get("version"))


def latest_release(opener: Callable[..., object] = urlopen) -> Release:
    """Fetch the latest stable release from the fixed GitHub repository."""
    request = Request(
        LATEST_RELEASE_URL,
        headers={
            "Accept": "application/vnd.github+json",
            "User-Agent": "ytqjk-dashboard-updater",
        },
    )
    try:
        with opener(request, timeout=15) as response:
            payload = json.loads(
                _read_limited(response, MAX_METADATA_BYTES).decode("utf-8")
            )
    except (HTTPError, URLError, OSError, UnicodeDecodeError,
            json.JSONDecodeError) as error:
        raise UpdateError("无法读取 GitHub 最新版本。") from error
    if not isinstance(payload, dict):
        raise UpdateError("GitHub 版本响应无效。")
    return _release(payload)


def check_update(
    plugin_root: Path,
    loader: Callable[[], Release] = latest_release,
) -> dict[str, object]:
    """Return a path-free version comparison for the dashboard."""
    installed = current_version(plugin_root)
    release = loader()
    return {
        "current_version": installed,
        "latest_version": release.version,
        "update_available": _version_key(release.version) > _version_key(installed),
        "release_url": release.page_url,
    }


def perform_update(
    plugin_root: Path,
    loader: Callable[[], Release] = latest_release,
    downloader: Callable[[Release, Path], None] | None = None,
    installer: Callable[[Path, Path, str], dict[str, object]] | None = None,
) -> dict[str, object]:
    """Download, validate, and atomically install the latest release."""
    installed = current_version(plugin_root)
    release = loader()
    if _version_key(release.version) <= _version_key(installed):
        return {
            "status": "UP_TO_DATE",
            "current_version": installed,
            "latest_version": release.version,
            "restart_required": False,
        }
    codex_root = _managed_codex_root(plugin_root)
    with tempfile.TemporaryDirectory(prefix="ytqjk-update-") as temporary:
        root = Path(temporary)
        archive = root / "release.zip"
        (downloader or download_archive)(release, archive)
        source = extract_release(archive, root / "source", release.version)
        receipt = (installer or run_installer)(
            source, codex_root, release.version
        )
    return {
        "status": "UPDATED",
        "current_version": installed,
        "latest_version": release.version,
        "restart_required": True,
        "apply_status": receipt["apply"]["status"],
    }


def download_archive(
    release: Release,
    destination: Path,
    opener: Callable[..., object] = urlopen,
) -> None:
    """Download one bounded release archive from approved GitHub hosts."""
    _github_url(release.archive_url, {"api.github.com"})
    request = Request(
        release.archive_url,
        headers={"Accept": "application/vnd.github+json",
                 "User-Agent": "ytqjk-dashboard-updater"},
    )
    try:
        with opener(request, timeout=60) as response:
            _github_url(
                response.geturl(),
                {"api.github.com", "codeload.github.com", "github.com"},
            )
            destination.write_bytes(
                _read_limited(response, MAX_ARCHIVE_BYTES)
            )
    except (HTTPError, URLError, OSError) as error:
        raise UpdateError("GitHub 更新包下载失败。") from error


def extract_release(
    archive: Path, destination: Path, expected_version: str
) -> Path:
    """Safely extract and validate the expected marketplace release."""
    destination.mkdir(parents=True, exist_ok=False)
    total = 0
    roots: set[str] = set()
    try:
        with zipfile.ZipFile(archive) as package:
            members = package.infolist()
            if len(members) > MAX_ARCHIVE_FILES:
                raise UpdateError("GitHub 更新包文件过多。")
            for info in members:
                parts = PurePosixPath(info.filename).parts
                if parts and ":" in parts[0]:
                    raise UpdateError("GitHub 更新包顶层目录无效。")
                mode = info.external_attr >> 16
                file_type = stat.S_IFMT(mode)
                unsafe = (
                    not parts or "\\" in info.filename or ".." in parts
                    or PurePosixPath(info.filename).is_absolute()
                    or any(":" in part for part in parts)
                    or file_type not in (0, stat.S_IFREG, stat.S_IFDIR)
                    or stat.S_ISLNK(mode) or bool(info.flag_bits & 1)
                )
                if unsafe:
                    raise UpdateError("GitHub 更新包包含不安全路径。")
                roots.add(parts[0])
                total += info.file_size
                if total > MAX_EXTRACTED_BYTES:
                    raise UpdateError("GitHub 更新包解压后过大。")
                target = destination.joinpath(*parts)
                if info.is_dir() or info.filename.endswith("/"):
                    target.mkdir(parents=True, exist_ok=True)
                    continue
                target.parent.mkdir(parents=True, exist_ok=True)
                with package.open(info) as source, target.open("xb") as output:
                    shutil.copyfileobj(source, output)
    except (OSError, zipfile.BadZipFile) as error:
        raise UpdateError("GitHub 更新包无效。") from error
    if len(roots) != 1:
        raise UpdateError("GitHub 更新包目录结构无效。")
    top_level = next(iter(roots))
    if (
        re.fullmatch(r"[A-Za-z0-9._-]+", top_level) is None
        or "ytqjk-marketplace" not in top_level
    ):
        raise UpdateError("GitHub 更新包顶层目录无效。")
    source_root = destination / top_level
    _validate_source(source_root, expected_version)
    return source_root


def run_installer(
    source_root: Path, codex_root: Path, expected_version: str
) -> dict[str, object]:
    """Run the release's transactional installer for managed plugins only."""
    command = [
        sys.executable, str(source_root / "setup.py"),
        "--mode", "codex-only", "--target-root", str(source_root),
        "--codex-root", str(codex_root), "--codex-import", "off",
        "--project-bootstrap", "off", "--dashboard-service", "off",
        "--apply", "--yes", "--json",
    ]
    options: dict[str, object] = {}
    if sys.platform == "win32":
        options["creationflags"] = CREATE_NO_WINDOW
    try:
        completed = subprocess.run(
            command, cwd=source_root, env=os.environ.copy(),
            capture_output=True, text=True, encoding="utf-8",
            errors="replace", timeout=900, check=False,
            **options,
        )
    except (OSError, subprocess.SubprocessError) as error:
        raise UpdateError("无法启动更新安装器。") from error
    if completed.returncode != 0:
        failure = _receipt(completed.stderr)
        action = failure.get("failed_action") if failure else None
        safe_action = (
            action if isinstance(action, str)
            and re.fullmatch(r"[A-Za-z0-9:_-]+", action) else None
        )
        detail = f"，失败步骤 {safe_action}" if safe_action else ""
        raise UpdateError(
            f"更新安装失败（退出码 {completed.returncode}{detail}）。"
        )
    receipt = _receipt(completed.stdout)
    if (
        receipt is None
        or receipt.get("version") != expected_version
        or not isinstance(receipt.get("apply"), dict)
        or receipt["apply"].get("status") != "APPLIED"
    ):
        raise UpdateError("更新安装器未返回有效成功回执。")
    return receipt


def _release(payload: dict[str, object]) -> Release:
    if payload.get("draft") is not False or payload.get("prerelease") is not False:
        raise UpdateError("GitHub 最新版本不是正式发布。")
    tag = payload.get("tag_name")
    archive = payload.get("zipball_url")
    page = payload.get("html_url")
    if not all(isinstance(value, str) for value in (tag, archive, page)):
        raise UpdateError("GitHub 版本字段无效。")
    version = _version(tag[1:] if tag.startswith("v") else tag)
    _github_url(archive, {"api.github.com"})
    expected_archive = f"/repos/{REPOSITORY}/zipball/"
    if not urlparse(archive).path.startswith(expected_archive):
        raise UpdateError("GitHub 更新地址无效。")
    expected_page = f"https://github.com/{REPOSITORY}/releases/tag/"
    if not page.startswith(expected_page):
        raise UpdateError("GitHub 发布页面无效。")
    return Release(version, tag, archive, page)


def _validate_source(source: Path, version: str) -> None:
    if not (source / "setup.py").is_file():
        raise UpdateError("GitHub 更新包缺少安装器。")
    for name in PLUGIN_NAMES:
        manifest = source / "plugins" / name / ".codex-plugin" / "plugin.json"
        try:
            data = json.loads(manifest.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise UpdateError("GitHub 更新包插件清单无效。") from error
        if data.get("name") != name or data.get("version") != version:
            raise UpdateError("GitHub 更新包版本不一致。")


def _managed_codex_root(plugin_root: Path) -> Path:
    root = plugin_root.resolve()
    if root.name != PLUGIN_NAMES[0] or root.parent.name != "plugins":
        raise UpdateError("当前插件不是稳定安装目录。")
    managed = root.parent / ".ytqjk-managed.json"
    if not managed.is_file() or managed.is_symlink():
        raise UpdateError("当前插件不受 YTQJK 安装器管理。")
    return root.parent.parent


def _version(value: object) -> str:
    if not isinstance(value, str):
        raise UpdateError("版本号无效。")
    parts = value.split(".")
    invalid = any(
        not part.isdigit() or len(part) > 1 and part.startswith("0")
        for part in parts
    )
    if len(parts) != 3 or invalid:
        raise UpdateError("版本号必须是纯 SemVer。")
    return value


def _version_key(value: str) -> tuple[int, int, int]:
    return tuple(int(part) for part in _version(value).split("."))


def _github_url(value: str, hosts: set[str]) -> None:
    parsed = urlparse(value)
    if parsed.scheme != "https" or parsed.hostname not in hosts:
        raise UpdateError("GitHub 更新地址无效。")


def _read_limited(response: object, limit: int) -> bytes:
    length = response.headers.get("Content-Length")
    try:
        if length and int(length) > limit:
            raise UpdateError("GitHub 响应过大。")
    except ValueError as error:
        raise UpdateError("GitHub 响应长度无效。") from error
    content = response.read(limit + 1)
    if len(content) > limit:
        raise UpdateError("GitHub 响应过大。")
    return content


def _receipt(output: str) -> dict[str, object] | None:
    for line in reversed(output.splitlines()):
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            return value
    return None
