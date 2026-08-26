from __future__ import annotations

import sys
from pathlib import Path
from unittest.mock import patch

import pytest


TESTS = Path(__file__).resolve().parent
SCRIPTS = TESTS.parent
DASHBOARD = SCRIPTS.parent / "dashboard"
sys.path[:0] = [str(TESTS), str(SCRIPTS), str(DASHBOARD)]

import document_runtime  # noqa: E402
from document_runtime import DocumentRuntime  # noqa: E402
from test_document_runtime import FakeRunner, PACKAGES  # noqa: E402


def test_build_reuses_captured_integrity_data(tmp_path: Path) -> None:
    root = tmp_path / "knowledge"
    runner = FakeRunner()
    manager = DocumentRuntime(
        root,
        runner=runner,
        platform_name="linux",
    )
    venv = root / "stage/venv"
    models = root / "stage/models"
    with (
        patch.object(
            document_runtime,
            "_runtime_digest",
            wraps=document_runtime._runtime_digest,
        ) as runtime_digest,
        patch.object(
            document_runtime,
            "inventory",
            wraps=document_runtime.inventory,
        ) as inventory_call,
    ):
        data = manager.build(venv, models)

    probe_calls = [
        command
        for command, _ in runner.calls
        if Path(command[-1]).name == "document_runtime_probe.py"
    ]
    model_scans = [
        call
        for call in inventory_call.call_args_list
        if call.args == (models,)
    ]
    assert len(probe_calls) == 1
    assert runtime_digest.call_count == 1
    assert len(model_scans) == 1
    assert data["packages"] == PACKAGES
    assert data["models"]["file_count"] > 0


@pytest.mark.parametrize(
    ("name", "code"),
    (
        ("manifest.json", "MODEL_MANIFEST_INVALID"),
        (".ytqjk-runtime.json", "RUNTIME_MARKER_INVALID"),
    ),
)
def test_build_requires_exact_document_readback(
    tmp_path: Path,
    name: str,
    code: str,
) -> None:
    root = tmp_path / "knowledge"
    manager = DocumentRuntime(
        root,
        runner=FakeRunner(),
        platform_name="linux",
    )
    original = document_runtime._write_json

    def corrupt(path: Path, value: dict[str, object]) -> None:
        original(path, value)
        if path.name == name:
            path.write_text("{}\n", encoding="utf-8")

    with (
        patch.object(document_runtime, "_write_json", side_effect=corrupt),
        pytest.raises(document_runtime.DocumentRuntimeError) as captured,
    ):
        manager.build(root / "stage/venv", root / "stage/models")

    assert captured.value.code == code
