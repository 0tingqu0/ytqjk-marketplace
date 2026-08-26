from __future__ import annotations

import json
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from document_runtime import DocumentRuntime, DocumentRuntimeError  # noqa: E402
from document_runtime_assets import (  # noqa: E402
    normalize_windows_downloads,
)
from document_runtime_downloads import (  # noqa: E402
    download_commands,
    windows_download_layout,
)


PACKAGES = {
    "docling": "2.121.0",
    "rapidocr": "3.9.2",
    "paddleocr": "3.7.0",
    "paddlepaddle": "3.3.1",
    "onnxruntime": "1.29.0",
    "transformers": "5.15.1",
    "torch": "2.13.0+cpu",
    "huggingface-hub": "1.28.0",
    "pypdfium2": "5.13.0",
    "Pillow": "12.3.0",
    "numpy": "2.3.5",
}


class FailingRunner:
    def __init__(self, stage: str) -> None:
        self.stage = stage

    def __call__(self, command: list[str], timeout: int) -> object:
        del timeout
        if command[1:4] == ["-m", "venv", "--copies"]:
            venv = Path(command[-1])
            for relative in ("bin/python", "bin/docling-tools", "bin/hf"):
                path = venv / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b"tool")
        if command[1:4] == ["-m", "pip", "install"]:
            code = 1 if self.stage == "package" else 0
            return _result(code)
        if Path(command[-1]).name == "document_runtime_probe.py":
            payload = json.dumps({
                "packages": PACKAGES,
                "distributions": list(PACKAGES),
                "imports": list(PACKAGES),
                "onnx_providers": ["CPUExecutionProvider"],
            })
            return _result(0, f"YTQJK_RUNTIME_PROBE={payload}")
        if len(command) > 2 and command[1] == "download":
            return _result(1 if self.stage == "model" else 0)
        return _result(0)


def _result(code: int, output: str = "") -> object:
    return SimpleNamespace(
        returncode=code,
        stdout=output,
        stderr="private-token-must-not-leak",
    )


def test_requirements_pin_direct_cpu_dependencies() -> None:
    content = (SCRIPTS / "requirements-document.txt").read_text(
        encoding="utf-8"
    )
    assert "onnxruntime-gpu" not in content
    assert "docling[rapidocr,vlm]==2.121.0" in content
    assert "https://download.pytorch.org/whl/cpu" in content
    for name, version in PACKAGES.items():
        if name == "docling":
            continue
        assert f"{name}=={version}" in content


def test_all_remote_model_downloads_have_commit_revisions() -> None:
    commands = download_commands(
        Path("/venv/bin/docling-tools"), Path("/m"), False
    )
    hf_commands = [command for command, _ in commands if "download" in command]
    assert len(hf_commands) == 12
    assert all("--revision" in command for command in hf_commands)
    revisions = [
        command[command.index("--revision") + 1]
        for command in hf_commands
    ]
    assert all(len(revision) == 40 for revision in revisions)
    assert all("models" not in command for command in hf_commands)
    assert Path(commands[-1][0][1]).name == "document_runtime_assets.py"


def test_windows_downloads_use_short_paths_then_normalize(
    tmp_path: Path,
) -> None:
    output = tmp_path / "m"
    commands = download_commands(
        Path("C:/v/Scripts/docling-tools.exe"),
        output,
        True,
    )
    hf_commands = [command for command, _ in commands if "download" in command]
    local_dirs = [
        Path(command[command.index("--local-dir") + 1])
        for command in hf_commands
    ]
    assert all(path.parent.name == ".w" for path in local_dirs)
    assert "--windows-layout" in commands[-1][0]

    layout = windows_download_layout()
    for source, _ in layout:
        payload = output / source / "model.bin"
        payload.parent.mkdir(parents=True)
        payload.write_bytes(b"model")
    cache = output / layout[0][0] / ".cache/huggingface/download.lock"
    cache.parent.mkdir(parents=True)
    cache.write_bytes(b"lock")
    normalize_windows_downloads(output)

    assert not (output / ".w").exists()
    for _, target in layout:
        assert (output / target / "model.bin").read_bytes() == b"model"
        assert not (output / target / ".cache").exists()


@pytest.mark.parametrize(
    ("stage", "code"),
    (
        ("package", "PACKAGE_INSTALL_FAILED"),
        ("model", "MODEL_DOWNLOAD_FAILED"),
    ),
)
def test_failures_are_stable_and_do_not_leak_stderr(
    tmp_path: Path,
    stage: str,
    code: str,
) -> None:
    root = tmp_path / "root"
    manager = DocumentRuntime(
        root,
        runner=FailingRunner(stage),
        platform_name="linux",
    )
    with pytest.raises(DocumentRuntimeError) as captured:
        manager.build(root / "stage/venv", root / "stage/models")
    assert captured.value.code == code
    assert str(captured.value) == code
    assert "private-token" not in str(captured.value)
