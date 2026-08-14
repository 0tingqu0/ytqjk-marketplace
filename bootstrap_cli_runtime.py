"""Bootstrap a user-scoped Node.js and Codex CLI runtime on Windows."""
from __future__ import annotations

import hashlib
import os
import platform
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import sys
import tempfile
from typing import Callable, Mapping
import zipfile

from runtime_download import download_node_file
from system_command_probe import unavailable_commands
from windows_paths import remove_tree

NODE_VERSION = "24.15.0"
CODEX_VERSION = "0.147.0"
NODE_ARCHIVE = f"node-v{NODE_VERSION}-win-x64.zip"
NODE_BASE_URL = f"https://nodejs.org/dist/v{NODE_VERSION}"
MAX_EXTRACTED_BYTES = 512 * 1024 * 1024

Downloader = Callable[[str, Path], None]
Executor = Callable[
    [list[str], dict[str, str]], subprocess.CompletedProcess[str]
]


@dataclass(frozen=True)
class CliRuntime:
    status: str
    root: Path | None
    executables: Mapping[str, str]
    environment: dict[str, str] | None
    provisioned: tuple[str, ...] = ()

    def receipt(self) -> dict[str, object]:
        result: dict[str, object] = {
            "status": self.status,
            "root": str(self.root) if self.root else None,
            "provisioned": list(self.provisioned),
        }
        if self.root:
            result.update({
                "node_version": NODE_VERSION,
                "codex_version": CODEX_VERSION,
            })
        return result


def default_runtime_root(
    environment: Mapping[str, str] | None = None,
    system: str | None = None,
) -> Path:
    environment = environment or os.environ
    current_system = system or platform.system()
    if current_system != "Windows":
        raise RuntimeError("automatic CLI bootstrap is currently Windows-only")
    local_app_data = environment.get("LOCALAPPDATA")
    if not local_app_data:
        raise RuntimeError("LOCALAPPDATA is required for CLI bootstrap")
    return Path(local_app_data) / "YTQJK" / "runtime"


def _execute(
    command: list[str], environment: dict[str, str]
) -> subprocess.CompletedProcess[str]:
    checking_version = command[-1:] == ["--version"]
    options: dict[str, object] = {
        "check": True,
        "text": True,
        "shell": False,
        "env": environment,
    }
    if checking_version:
        options["capture_output"] = True
    else:
        options["stdout"] = sys.stderr
        options["stderr"] = sys.stderr
    return subprocess.run(command, **options)


def _is_reparse(path: Path) -> bool:
    if path.is_symlink():
        return True
    attributes = getattr(path.lstat(), "st_file_attributes", 0)
    return bool(attributes & 0x400)


def _assert_plain_tree(root: Path) -> None:
    if _is_reparse(root):
        raise RuntimeError("runtime path contains a reparse point")
    for item in root.rglob("*"):
        if _is_reparse(item):
            raise RuntimeError("runtime path contains a reparse point")


def _plain_files(root: Path, paths: tuple[Path, ...]) -> bool:
    try:
        if _is_reparse(root):
            return False
        return all(path.is_file() and not _is_reparse(path) for path in paths)
    except OSError:
        return False


def _remove_managed(path: Path, root: Path) -> None:
    if not path.exists():
        return
    try:
        path.resolve().relative_to(root.resolve())
    except ValueError as error:
        raise RuntimeError("runtime path escapes managed root") from error
    _assert_plain_tree(path)
    remove_tree(path)


def _extract_node_archive(archive_path: Path, destination: Path) -> Path:
    allowed = {
        NODE_ARCHIVE,
        f"node-v{NODE_VERSION}-win-arm64.zip",
    }
    if archive_path.name not in allowed:
        raise RuntimeError("unsafe Node.js archive name")
    expected_root = archive_path.name.removesuffix(".zip")
    destination.mkdir(parents=True, exist_ok=False)
    total = 0
    with zipfile.ZipFile(archive_path) as archive:
        for info in archive.infolist():
            name = info.filename
            parts = PurePosixPath(name).parts
            mode = info.external_attr >> 16
            file_type = stat.S_IFMT(mode)
            unsafe_type = file_type not in (0, stat.S_IFREG, stat.S_IFDIR)
            unsafe_path = (
                not parts
                or "\\" in name
                or PurePosixPath(name).is_absolute()
                or ".." in parts
                or parts[0] != expected_root
            )
            if unsafe_path or unsafe_type or stat.S_ISLNK(mode):
                raise RuntimeError("unsafe Node.js archive member")
            total += info.file_size
            if total > MAX_EXTRACTED_BYTES:
                raise RuntimeError("unsafe Node.js archive size")
            target = destination.joinpath(*parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            if info.is_dir() or name.endswith("/"):
                target.mkdir(parents=True, exist_ok=True)
                continue
            with archive.open(info) as source, target.open("xb") as output:
                shutil.copyfileobj(source, output)
    extracted = destination / expected_root
    _assert_plain_tree(extracted)
    return extracted


def _archive_name(machine: str) -> str:
    normalized = machine.lower()
    if normalized in ("amd64", "x86_64"):
        return NODE_ARCHIVE
    if normalized in ("arm64", "aarch64"):
        return f"node-v{NODE_VERSION}-win-arm64.zip"
    raise RuntimeError(f"unsupported Windows architecture: {machine}")


def _expected_checksum(checksums: Path, archive_name: str) -> str:
    for line in checksums.read_text(encoding="utf-8").splitlines():
        fields = line.split()
        if len(fields) == 2 and fields[1].lstrip("*") == archive_name:
            digest = fields[0].lower()
            if len(digest) == 64 and all(c in "0123456789abcdef" for c in digest):
                return digest
    raise RuntimeError("Node.js checksum is missing")


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        while chunk := source.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def _runtime_environment(
    root: Path, node_dir: Path, codex_dir: Path,
    environment: Mapping[str, str],
) -> dict[str, str]:
    result = dict(environment)
    entries = [str(node_dir), str(codex_dir)]
    if result.get("PATH"):
        entries.append(result["PATH"])
    result["PATH"] = os.pathsep.join(entries)
    result["npm_config_update_notifier"] = "false"
    return result


def _valid_node(
    node_dir: Path, environment: dict[str, str], executor: Executor
) -> bool:
    required = (node_dir / "node.exe", node_dir / "npm.cmd", node_dir / "npx.cmd")
    if not _plain_files(node_dir, required):
        return False
    try:
        result = executor([str(required[0]), "--version"], environment)
    except (OSError, subprocess.SubprocessError):
        return False
    return result.stdout.strip() == f"v{NODE_VERSION}"


def _valid_codex(
    codex_dir: Path, environment: dict[str, str], executor: Executor
) -> bool:
    executable = codex_dir / "codex.cmd"
    if not _plain_files(codex_dir, (executable,)):
        return False
    try:
        result = executor([str(executable), "--version"], environment)
    except (OSError, subprocess.SubprocessError):
        return False
    return CODEX_VERSION in result.stdout


def _install_node(
    root: Path, archive_name: str, downloader: Downloader
) -> Path:
    temporary = Path(tempfile.mkdtemp(prefix=".node-", dir=root))
    try:
        checksums = temporary / "SHASUMS256.txt"
        archive_path = temporary / archive_name
        downloader(f"{NODE_BASE_URL}/SHASUMS256.txt", checksums)
        downloader(f"{NODE_BASE_URL}/{archive_name}", archive_path)
        expected = _expected_checksum(checksums, archive_name)
        actual = _sha256_file(archive_path)
        if actual != expected:
            raise RuntimeError("Node.js archive checksum mismatch")
        unpacked = _extract_node_archive(archive_path, temporary / "unpacked")
        final = root / archive_name.removesuffix(".zip")
        _remove_managed(final, root)
        unpacked.replace(final)
        return final
    finally:
        shutil.rmtree(temporary, ignore_errors=True)


def _install_codex(
    root: Path, node_dir: Path, environment: dict[str, str], executor: Executor
) -> Path:
    temporary = Path(tempfile.mkdtemp(prefix=".codex-", dir=root))
    prefix = temporary / "prefix"
    try:
        install_environment = environment.copy()
        install_environment["npm_config_cache"] = str(temporary / "npm-cache")
        command = [
            str(node_dir / "npm.cmd"), "install", "--global",
            "--prefix", str(prefix), "--no-audit", "--no-fund",
            f"@openai/codex@{CODEX_VERSION}",
        ]
        executor(command, install_environment)
        if not _valid_codex(prefix, environment, executor):
            raise RuntimeError("Codex CLI validation failed")
        final = root / f"codex-{CODEX_VERSION}"
        _remove_managed(final, root)
        prefix.replace(final)
        return final
    except (OSError, subprocess.SubprocessError) as error:
        raise RuntimeError("Codex CLI bootstrap failed") from error
    finally:
        shutil.rmtree(temporary, ignore_errors=True)


def ensure_cli_runtime(
    required: set[str], *, runtime_root: Path | None = None,
    system: str | None = None, machine: str | None = None,
    environment: Mapping[str, str] | None = None,
    which: Callable[[str], str | None] = shutil.which,
    downloader: Downloader = download_node_file, executor: Executor = _execute,
) -> CliRuntime:
    base_environment = environment or os.environ
    missing = unavailable_commands(
        required, base_environment, which, executor
    )
    if not missing:
        return CliRuntime("SYSTEM", None, {}, None)
    current_system = system or platform.system()
    if current_system != "Windows":
        names = ", ".join(sorted(missing))
        raise RuntimeError(f"required command not found: {names}")
    root = (runtime_root or default_runtime_root(environment, current_system)).resolve()
    root.mkdir(parents=True, exist_ok=True)
    if _is_reparse(root):
        raise RuntimeError("runtime path contains a reparse point")
    archive_name = _archive_name(machine or platform.machine())
    node_dir = root / archive_name.removesuffix(".zip")
    codex_dir = root / f"codex-{CODEX_VERSION}"
    runtime_environment = _runtime_environment(
        root, node_dir, codex_dir, base_environment
    )
    provisioned: list[str] = []
    if not _valid_node(node_dir, runtime_environment, executor):
        print("YTQJK: 正在下载并校验便携 Node.js。", file=sys.stderr)
        node_dir = _install_node(root, archive_name, downloader)
        provisioned.append("node")
        runtime_environment = _runtime_environment(
            root, node_dir, codex_dir, base_environment
        )
        if not _valid_node(node_dir, runtime_environment, executor):
            raise RuntimeError("Node.js runtime validation failed")
    if "codex" in missing and not _valid_codex(
        codex_dir, runtime_environment, executor
    ):
        print("YTQJK: 正在安装固定版本 Codex CLI。", file=sys.stderr)
        codex_dir = _install_codex(
            root, node_dir, runtime_environment, executor
        )
        provisioned.append("codex")
        runtime_environment = _runtime_environment(
            root, node_dir, codex_dir, base_environment
        )
    executables: dict[str, str] = {
        "node": str(node_dir / "node.exe"),
        "npm": str(node_dir / "npm.cmd"),
    }
    if "npx" in missing:
        executables["npx"] = str(node_dir / "npx.cmd")
    if "codex" in missing:
        executables["codex"] = str(codex_dir / "codex.cmd")
    status = "BOOTSTRAPPED" if provisioned else "REUSED"
    return CliRuntime(
        status, root, executables, runtime_environment, tuple(provisioned)
    )
