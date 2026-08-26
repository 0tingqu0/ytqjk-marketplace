"""Build and verify an isolated local document runtime."""

from __future__ import annotations

import hashlib
import json
import os
import subprocess
import sys
import uuid
from pathlib import Path
from typing import Callable

from document_runtime_downloads import download_commands
from document_runtime_integrity import RuntimeIntegrityError
from document_runtime_integrity import runtime_tree_sha256, validate_cpu_probe
from document_runtime_models import RuntimeModelError
from document_runtime_models import build_manifest, validate_manifest
from path_safety import is_reparse
from stable_file import StableFileError, snapshot_tree


SCHEMA_VERSION = 2
DOCLING_VERSION = "2.121.0"
RAPIDOCR_VERSION = "3.9.2"
PADDLEOCR_VERSION = "3.7.0"
PADDLEPADDLE_VERSION = "3.3.1"
ONNXRUNTIME_VERSION = "1.29.0"
TRANSFORMERS_VERSION = "5.15.1"
TORCH_VERSION = "2.13.0+cpu"
HUGGINGFACE_HUB_VERSION = "1.28.0"
PYPDFIUM2_VERSION = "5.13.0"
PILLOW_VERSION = "12.3.0"
NUMPY_VERSION = "2.3.5"
MAX_FILES = 100_000
MAX_BYTES = 32 * 1024 * 1024 * 1024
Runner = Callable[[list[str], int], object]
Downloader = Callable[[Path, Path], None]
_PROBE_SENTINEL = "YTQJK_RUNTIME_PROBE="
_VERSIONS = {
    "docling": DOCLING_VERSION,
    "rapidocr": RAPIDOCR_VERSION,
    "paddleocr": PADDLEOCR_VERSION,
    "paddlepaddle": PADDLEPADDLE_VERSION,
    "onnxruntime": ONNXRUNTIME_VERSION,
    "transformers": TRANSFORMERS_VERSION,
    "torch": TORCH_VERSION,
    "huggingface-hub": HUGGINGFACE_HUB_VERSION,
    "pypdfium2": PYPDFIUM2_VERSION,
    "Pillow": PILLOW_VERSION,
    "numpy": NUMPY_VERSION,
}


class DocumentRuntimeError(RuntimeError):
    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


class DocumentRuntime:
    def __init__(
        self, root: Path, *,
        requirements: Path | None = None,
        runner: Runner | None = None,
        downloader: Downloader | None = None,
        platform_name: str | None = None,
    ) -> None:
        raw_root = Path(root).absolute()
        reject_reparse_chain(raw_root)
        self.root = raw_root.resolve()
        self.runtime = self.root / ".runtime" / "document-intake"
        self.venv = self.runtime / "venv"
        self.models = self.root / "models" / "document-intake"
        default = Path(__file__).with_name("requirements-document.txt")
        self.requirements = requirements or default
        self.runner = runner or _default_runner
        self.downloader = downloader or self._official_download
        self.platform = platform_name or sys.platform

    def build(self, venv: Path, models: Path) -> dict[str, object]:
        require_file(self.requirements, "REQUIREMENTS_UNAVAILABLE")
        require_contained(venv, self.root)
        require_contained(models, self.root)
        prepare_directory(venv.parent)
        prepare_directory(models.parent)
        command = [sys.executable, "-m", "venv", "--copies", str(venv)]
        self._execute(command, 600, "VENV_CREATION_FAILED")
        python = self.python_path(venv)
        require_file(python, "VENV_CREATION_FAILED")
        command = [
            str(python), "-m", "pip", "install", "--no-input",
            "--disable-pip-version-check", "-r", str(self.requirements),
        ]
        self._execute(command, 3600, "PACKAGE_INSTALL_FAILED")
        packages = self._probe(venv)
        tool = self.tool_path(venv)
        require_file(tool, "DOCLING_TOOLS_MISSING")
        try:
            self.downloader(tool, models)
        except DocumentRuntimeError:
            raise
        except Exception as error:
            raise DocumentRuntimeError("MODEL_DOWNLOAD_FAILED") from error
        model_data = self._write_manifest(models)
        requirements_sha = digest_file(self.requirements)
        runtime_sha = _runtime_digest(venv)
        marker = {
            "schema_version": SCHEMA_VERSION,
            "requirements_sha256": requirements_sha,
            "venv_tree_sha256": runtime_sha,
        }
        marker_path = venv / ".ytqjk-runtime.json"
        _write_verified_json(marker_path, marker, "RUNTIME_MARKER_INVALID")
        return {
            "python": str(python),
            "requirements_sha256": requirements_sha,
            "packages": packages,
            "models": model_data,
        }

    def ready_data(
        self, venv: Path | None = None,
        models: Path | None = None,
    ) -> dict[str, object]:
        checked_venv = venv or self.venv
        checked_models = models or self.models
        reject_reparse_chain(self.root)
        require_contained(checked_venv, self.root)
        require_contained(checked_models, self.root)
        marker = _load_json(checked_venv / ".ytqjk-runtime.json")
        expected = {"schema_version", "requirements_sha256"}
        expected.add("venv_tree_sha256")
        invalid_schema = marker.get("schema_version") != SCHEMA_VERSION
        if set(marker) != expected or invalid_schema:
            raise DocumentRuntimeError("RUNTIME_MARKER_INVALID")
        requirements_sha = digest_file(self.requirements)
        if marker["requirements_sha256"] != requirements_sha:
            raise DocumentRuntimeError("REQUIREMENTS_CHANGED")
        if marker["venv_tree_sha256"] != _runtime_digest(checked_venv):
            raise DocumentRuntimeError("RUNTIME_INTEGRITY_MISMATCH")
        return {
            "python": str(self.python_path(checked_venv)),
            "requirements_sha256": requirements_sha,
            "packages": self._probe(checked_venv),
            "models": self._validate_models(checked_models),
        }

    def _probe(self, venv: Path) -> dict[str, str]:
        python = self.python_path(venv)
        require_file(python, "VENV_PYTHON_MISSING")
        probe = Path(__file__).with_name("document_runtime_probe.py")
        require_file(probe, "PACKAGE_PROBE_SCRIPT_MISSING")
        command = [str(python), "-I", "-X", "utf8", str(probe)]
        output = self._execute(command, 60, "PACKAGE_PROBE_FAILED")
        records = [
            line.removeprefix(_PROBE_SENTINEL)
            for line in output.splitlines()
            if line.startswith(_PROBE_SENTINEL)
        ]
        if len(records) != 1:
            raise DocumentRuntimeError("PACKAGE_PROBE_INVALID")
        try:
            value = json.loads(records[0])
        except (TypeError, json.JSONDecodeError) as error:
            raise DocumentRuntimeError("PACKAGE_PROBE_INVALID") from error
        try:
            return validate_cpu_probe(value, _VERSIONS)
        except RuntimeIntegrityError as error:
            raise DocumentRuntimeError(str(error)) from error

    def _official_download(self, tool: Path, output: Path) -> None:
        windows = self.platform == "win32"
        commands = download_commands(tool, output, windows)
        for command, timeout in commands:
            require_file(Path(command[0]), "MODEL_TOOL_MISSING")
            self._execute(command, timeout, "MODEL_DOWNLOAD_FAILED")

    def _write_manifest(self, root: Path) -> dict[str, object]:
        files = _model_files(root)
        try:
            manifest = build_manifest(files)
        except RuntimeModelError as error:
            raise DocumentRuntimeError(error.code) from error
        manifest_path = root / "manifest.json"
        _write_verified_json(manifest_path, manifest, "MODEL_MANIFEST_INVALID")
        try:
            return validate_manifest(manifest, files)
        except RuntimeModelError as error:
            raise DocumentRuntimeError(error.code) from error

    def _validate_models(self, root: Path) -> dict[str, object]:
        manifest = _load_json(root / "manifest.json")
        files = _model_files(root)
        try:
            return validate_manifest(manifest, files)
        except RuntimeModelError as error:
            raise DocumentRuntimeError(error.code) from error

    def _execute(
        self, command: list[str], timeout: int,
        failure_code: str = "COMMAND_FAILED",
    ) -> str:
        try:
            result = self.runner(list(command), timeout)
            code = getattr(result, "returncode", None)
            output = getattr(result, "stdout", "")
        except Exception as error:
            raise DocumentRuntimeError(failure_code) from error
        if type(code) is not int or code != 0 or type(output) is not str:
            raise DocumentRuntimeError(failure_code)
        return output.strip()

    def python_path(self, venv: Path) -> Path:
        folder = "Scripts" if self.platform == "win32" else "bin"
        name = "python.exe" if self.platform == "win32" else "python"
        return venv / folder / name

    def tool_path(self, venv: Path) -> Path:
        folder = "Scripts" if self.platform == "win32" else "bin"
        if self.platform == "win32":
            return venv / folder / "docling-tools.exe"
        return venv / folder / "docling-tools"


def _default_runner(command: list[str], timeout: int) -> object:
    return subprocess.run(
        command,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        check=False,
        timeout=timeout,
    )


def _load_json(path: Path) -> dict[str, object]:
    require_file(path, "RUNTIME_DOCUMENT_MISSING")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise DocumentRuntimeError("RUNTIME_DOCUMENT_INVALID") from error
    if type(value) is not dict:
        raise DocumentRuntimeError("RUNTIME_DOCUMENT_INVALID")
    return value


def _write_json(path: Path, value: dict[str, object]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    try:
        content = json.dumps(value, ensure_ascii=False, sort_keys=True)
        temporary.write_text(content + "\n", encoding="utf-8")
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def _write_verified_json(
    path: Path,
    value: dict[str, object],
    code: str,
) -> None:
    _write_json(path, value)
    if _load_json(path) != value:
        raise DocumentRuntimeError(code)


def _model_files(root: Path) -> dict[str, str]:
    files = inventory(root)
    files.pop("manifest.json", None)
    if not files:
        raise DocumentRuntimeError("MODEL_OUTPUT_EMPTY")
    return files


def inventory(root: Path) -> dict[str, str]:
    try:
        tree = snapshot_tree(root, MAX_FILES, MAX_BYTES)
    except StableFileError as error:
        code = str(error)
        if code in {"TREE_TOO_LARGE", "FILE_TOO_LARGE"}:
            raise DocumentRuntimeError("RUNTIME_TOO_LARGE") from error
        raise DocumentRuntimeError("UNSAFE_RUNTIME_PATH") from error
    return dict(sorted(tree.hashes.items()))


def _runtime_digest(root: Path) -> str:
    try:
        return runtime_tree_sha256(root, MAX_FILES, MAX_BYTES)
    except StableFileError as error:
        code = str(error)
        if code in {"TREE_TOO_LARGE", "FILE_TOO_LARGE"}:
            raise DocumentRuntimeError("RUNTIME_TOO_LARGE") from error
        raise DocumentRuntimeError("UNSAFE_RUNTIME_PATH") from error


def digest_file(path: Path) -> str:
    require_file(path, "RUNTIME_FILE_MISSING")
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def require_file(path: Path, code: str) -> None:
    if not path.is_file() or is_reparse(path):
        raise DocumentRuntimeError(code)


def prepare_directory(path: Path) -> None:
    reject_reparse_chain(path)
    path.mkdir(parents=True, exist_ok=True)
    if not path.is_dir() or is_reparse(path):
        raise DocumentRuntimeError("UNSAFE_RUNTIME_PATH")


def reject_reparse_chain(path: Path) -> None:
    for candidate in (path, *path.parents):
        if candidate.exists() and is_reparse(candidate):
            raise DocumentRuntimeError("UNSAFE_RUNTIME_PATH")


def require_contained(path: Path, parent: Path) -> None:
    try:
        path.resolve().relative_to(parent.resolve())
    except (OSError, ValueError) as error:
        raise DocumentRuntimeError("RUNTIME_PATH_ESCAPE") from error
