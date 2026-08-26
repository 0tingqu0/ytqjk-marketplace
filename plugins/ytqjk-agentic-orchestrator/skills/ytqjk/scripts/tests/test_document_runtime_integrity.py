from __future__ import annotations

import json
import sys
import tempfile
from pathlib import Path
from types import SimpleNamespace

import pytest


TESTS = Path(__file__).resolve().parent
SCRIPTS = TESTS.parent
DASHBOARD = SCRIPTS.parent / "dashboard"
sys.path[:0] = [str(TESTS), str(SCRIPTS), str(DASHBOARD)]

from document_runtime_service import (  # noqa: E402
    check_document_runtime,
    install_document_runtime,
)
from document_runtime_integrity import (  # noqa: E402
    forbidden_gpu_distributions,
)
from test_document_runtime import FakeRunner, PACKAGES  # noqa: E402


class ProbeRunner(FakeRunner):
    def __init__(
        self,
        missing_import: str | None = None,
        providers: tuple[str, ...] = ("CPUExecutionProvider",),
    ) -> None:
        super().__init__()
        self.missing_import = missing_import
        self.providers = providers

    def __call__(self, command: list[str], timeout: int) -> object:
        if Path(command[-1]).name != "document_runtime_probe.py":
            return super().__call__(command, timeout)
        self.calls.append((list(command), timeout))
        imports = [
            name for name in PACKAGES if name != self.missing_import
        ]
        payload = json.dumps({
            "packages": PACKAGES,
            "distributions": list(PACKAGES),
            "imports": imports,
            "onnx_providers": list(self.providers),
        })
        return SimpleNamespace(
            returncode=0,
            stdout=f"YTQJK_RUNTIME_PROBE={payload}",
        )


@pytest.fixture
def runtime_root() -> Path:
    with tempfile.TemporaryDirectory(prefix="ytqjk-") as temporary:
        yield Path(temporary) / "knowledge"


def test_package_source_tamper_is_not_configured(
    runtime_root: Path,
) -> None:
    root = runtime_root
    runner = FakeRunner()
    installed = install_document_runtime(
        root,
        runner=runner,
        platform_name="linux",
    )
    assert installed["status"] == "READY", (installed, runner.calls)
    marker = root / ".runtime/document-intake/venv/.ytqjk-runtime.json"
    marker_value = json.loads(marker.read_text(encoding="utf-8"))
    assert len(marker_value["venv_tree_sha256"]) == 64
    source = (
        root / ".runtime/document-intake/venv/lib/python/"
        "site-packages/docling/runtime.py"
    )
    source.write_bytes(b"tampered")

    checked = check_document_runtime(
        root,
        runner=runner,
        platform_name="linux",
    )

    assert checked["status"] == "NOT_CONFIGURED"
    assert checked["reason"] == "RUNTIME_INTEGRITY_MISMATCH"


def test_regenerable_runtime_caches_do_not_break_integrity(
    runtime_root: Path,
) -> None:
    root = runtime_root
    runner = FakeRunner()
    installed = install_document_runtime(
        root,
        runner=runner,
        platform_name="linux",
    )
    assert installed["status"] == "READY", (installed, runner.calls)
    venv = root / ".runtime/document-intake/venv"
    cache = venv / "lib/python/site-packages/docling/__pycache__"
    cache.mkdir()
    (cache / "runtime.cpython-312.pyc").write_bytes(b"cache")
    local_cache = venv / ".cache"
    local_cache.mkdir()
    (local_cache / "runtime.json").write_text("{}", encoding="utf-8")

    checked = check_document_runtime(
        root,
        runner=runner,
        platform_name="linux",
    )

    assert checked["status"] == "READY"


@pytest.mark.parametrize(
    "relative",
    (
        "orphan.pyc",
        "lib/python/site-packages/orphan.pyc",
        "lib/python/site-packages/orphan.pyo",
        (
            "lib/python/site-packages/orphan/__pycache__/"
            "ghost.cpython-311.pyc"
        ),
    ),
)
def test_sourceless_bytecode_is_integrity_protected(
    runtime_root: Path,
    relative: str,
) -> None:
    runner = FakeRunner()
    installed = install_document_runtime(
        runtime_root,
        runner=runner,
        platform_name="linux",
    )
    assert installed["status"] == "READY", (installed, runner.calls)
    venv = runtime_root / ".runtime/document-intake/venv"
    bytecode = venv / relative
    bytecode.parent.mkdir(parents=True, exist_ok=True)
    bytecode.write_bytes(b"untrusted-bytecode")

    checked = check_document_runtime(
        runtime_root,
        runner=runner,
        platform_name="linux",
    )

    assert checked["status"] == "NOT_CONFIGURED"
    assert checked["reason"] == "RUNTIME_INTEGRITY_MISMATCH"


def test_gpu_distribution_residue_fails_closed(
    runtime_root: Path,
) -> None:
    root = runtime_root
    runner = FakeRunner(extra_distributions=("nvidia-cublas-cu12",))

    installed = install_document_runtime(
        root,
        runner=runner,
        platform_name="linux",
    )

    assert installed["status"] == "FAILED"
    assert installed["runtime_status"] == "NOT_CONFIGURED"
    assert installed["reason"] == "GPU_DISTRIBUTION_PRESENT"


@pytest.mark.parametrize(
    ("runner", "reason"),
    (
        (ProbeRunner(missing_import="docling"), "PACKAGE_IMPORT_FAILED"),
        (ProbeRunner(providers=()), "ONNX_CPU_PROVIDER_MISSING"),
    ),
)
def test_runtime_probe_fails_closed(
    runtime_root: Path,
    runner: ProbeRunner,
    reason: str,
) -> None:
    installed = install_document_runtime(
        runtime_root,
        runner=runner,
        platform_name="linux",
    )

    assert installed["status"] == "FAILED"
    assert installed["reason"] == reason
    probe_calls = [
        command
        for command, _ in runner.calls
        if Path(command[-1]).name == "document_runtime_probe.py"
    ]
    assert probe_calls
    assert probe_calls[0][1:4] == ["-I", "-X", "utf8"]


def test_all_gpu_distribution_families_are_rejected() -> None:
    residues = [
        "onnxruntime_gpu",
        "nvidia.cublas.cu12",
        "triton",
        "cuda_bindings",
        "cupy-cuda12x",
        "tensorrt-cu12",
    ]

    assert forbidden_gpu_distributions(residues) == [
        "cuda-bindings",
        "cupy-cuda12x",
        "nvidia-cublas-cu12",
        "onnxruntime-gpu",
        "tensorrt-cu12",
        "triton",
    ]
