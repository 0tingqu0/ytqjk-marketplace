from __future__ import annotations

import base64
import hashlib
import json
import sys
from dataclasses import dataclass
from enum import Enum
from pathlib import Path


DASHBOARD = Path(__file__).resolve().parents[2] / "dashboard"
PLUGIN_ROOT = DASHBOARD.parents[2]
sys.path.insert(0, str(DASHBOARD.parent / "scripts"))
sys.path.insert(0, str(DASHBOARD))

from dashboard_intake_api import DashboardIntakeApi  # noqa: E402
from dashboard_intake_worker import DocumentIntakeWorker  # noqa: E402
from knowledge_engine_locator import (  # noqa: E402
    EnginePlanner,
    locate_knowledge_engine,
)


class State(str, Enum):
    CANDIDATE = "CANDIDATE"


@dataclass(frozen=True)
class Metadata:
    title: str = "流程图"
    summary: str = "识别到流程"
    pages: tuple[dict[str, object], ...] = ({"number": 1},)
    blocks: tuple[dict[str, object], ...] = ()


@dataclass(frozen=True)
class Candidate:
    candidate_id: str = "a" * 64
    source_digest: str = hashlib.sha256(b"clean-image").hexdigest()
    state: State = State.CANDIDATE
    metadata: Metadata = Metadata()
    chunks: tuple[dict[str, object], ...] = ()


class IdleWorker:
    @staticmethod
    def kick() -> None:
        return


class CrashAfterCandidateAdvance:
    def __init__(self, store: object) -> None:
        self._store = store

    def __getattr__(self, name: str) -> object:
        return getattr(self._store, name)

    def advance(self, *args: object, **kwargs: object) -> object:
        result = self._store.advance(*args, **kwargs)
        if args[3] == "candidate-write":
            raise KeyboardInterrupt("injected crash")
        return result


def _payload() -> dict[str, str]:
    return {
        "name": "diagram.png",
        "content": base64.b64encode(b"clean-image").decode("ascii"),
        "encoding": "base64",
        "purpose": "识别流程图",
    }


def _service(root: Path) -> DashboardIntakeApi:
    return DashboardIntakeApi(root, PLUGIN_ROOT, worker=IdleWorker())


def _planner(*_args: object) -> Candidate:
    return Candidate()


def test_candidate_conflict_fails_closed_without_overwrite(
    tmp_path: Path,
) -> None:
    service = _service(tmp_path)
    job_id = service.submit(_payload())["job"]["id"]
    base = tmp_path / "personal-experience" / "candidates" \
        / "imports" / "structured"
    base.mkdir(parents=True)
    document = base / f"{'a' * 64}.md"
    document.write_text("existing candidate", encoding="utf-8")
    engine = locate_knowledge_engine(PLUGIN_ROOT)
    worker = DocumentIntakeWorker(
        tmp_path, engine, service.store, planner=_planner
    )

    assert worker.process_one()
    job = service.get(f"/api/intake/jobs/{job_id}")["job"]
    assert job["state"] == "FAILED"
    assert job["error"]["category"] == "WRITE_FAILED"
    assert job["result"]["status"] == "DUPLICATE_CANDIDATE"
    assert document.read_text(encoding="utf-8") == "existing candidate"
    assert not (base / f"{'a' * 64}.json").exists()
    assert not (base / "originals" / f"{'a' * 64}.png").exists()
    assert not (
        tmp_path / "personal-experience/candidates/imports/chunks"
    ).exists()


def test_original_conflict_fails_closed_without_overwrite(
    tmp_path: Path,
) -> None:
    service = _service(tmp_path)
    job_id = service.submit(_payload())["job"]["id"]
    base = tmp_path / "personal-experience" / "candidates" \
        / "imports" / "structured"
    original = base / "originals" / f"{'a' * 64}.png"
    original.parent.mkdir(parents=True)
    original.write_bytes(b"existing-original")
    engine = locate_knowledge_engine(PLUGIN_ROOT)
    worker = DocumentIntakeWorker(
        tmp_path, engine, service.store, planner=_planner
    )

    assert worker.process_one()
    job = service.get(f"/api/intake/jobs/{job_id}")["job"]
    assert job["state"] == "FAILED"
    assert job["result"]["status"] == "DUPLICATE_CANDIDATE"
    assert original.read_bytes() == b"existing-original"
    assert not (base / f"{'a' * 64}.md").exists()
    assert not (base / f"{'a' * 64}.json").exists()
    assert not (
        tmp_path / "personal-experience/candidates/imports/chunks"
    ).exists()


def test_complete_stage_recovers_exact_candidate_and_result(
    tmp_path: Path,
) -> None:
    engine = locate_knowledge_engine(PLUGIN_ROOT)
    runtime = tmp_path / ".runtime" / "document-intake"
    runtime.mkdir(parents=True)
    now = [0.0]
    store_class = engine.module(
        "document_intake_job_store"
    ).DocumentIntakeJobStore
    store = store_class(
        runtime / "jobs.sqlite3", lease_seconds=1,
        clock=lambda: now[0],
    )
    service = DashboardIntakeApi(
        tmp_path, PLUGIN_ROOT, worker=IdleWorker(), store=store
    )
    job_id = service.submit(_payload())["job"]["id"]
    crashing = DocumentIntakeWorker(
        tmp_path, engine, CrashAfterCandidateAdvance(store), planner=_planner
    )
    try:
        crashing.process_one()
    except KeyboardInterrupt:
        pass
    else:
        raise AssertionError("crash injection did not fire")
    assert store.get(job_id).stage == "complete"
    staging = tmp_path / store.get(job_id).payload["staging_ref"]
    assert staging.is_file()
    now[0] = 2.0
    assert store.recover_expired() == 1
    recovered = DocumentIntakeWorker(
        tmp_path, engine, store, planner=_planner
    )
    assert recovered.process_one()
    job = service.get(f"/api/intake/jobs/{job_id}")["job"]
    assert job["state"] == "SUCCEEDED"
    assert job["result"]["status"] == "CANDIDATE"
    assert job["result"]["attempt"] == 2
    assert not staging.exists()


def test_pdf_manifest_maps_rec_keys_only_at_docling_boundary(
    tmp_path: Path,
) -> None:
    engine = locate_knowledge_engine(PLUGIN_ROOT)
    model_root = tmp_path / "models" / "document-intake"
    model_root.mkdir(parents=True)
    rapid = {
        "det": "det.onnx", "cls": "cls.onnx",
        "rec": "rec.onnx", "rec_keys": "keys.txt",
    }
    files = {}
    for relative in rapid.values():
        content = relative.encode("ascii")
        (model_root / relative).write_bytes(content)
        files[relative] = hashlib.sha256(content).hexdigest()
    extras = (
        "DocumentFigureClassifier-v2.5/model.onnx",
        "DocumentFigureClassifier-v2.5/config.json",
        "DocumentFigureClassifier-v2.5/preprocessor_config.json",
        "PaddleOCR/det/inference.pdiparams",
        "PaddleOCR/det/inference.yml",
        "PaddleOCR/rec/inference.pdiparams",
        "PaddleOCR/rec/inference.yml",
        "HuggingFaceTB--SmolVLM-256M-Instruct/config.json",
        "HuggingFaceTB--SmolVLM-256M-Instruct/model.safetensors",
    )
    for relative in extras:
        content = relative.encode("ascii")
        path = model_root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        files[relative] = hashlib.sha256(content).hexdigest()
    (model_root / "manifest.json").write_text(
        json.dumps({
            "schema_version": 1, "files": files, "rapidocr": rapid,
            "paddleocr": {
                "text_detection_model_dir": "PaddleOCR/det",
                "text_recognition_model_dir": "PaddleOCR/rec",
            },
            "smolvlm": {
                "model_dir": "HuggingFaceTB--SmolVLM-256M-Instruct",
            },
        }),
        encoding="utf-8",
    )
    planner = EnginePlanner(tmp_path, engine)
    settings = planner._settings()
    assert settings is not None
    assert set(settings.rapidocr) == {"det", "cls", "rec", "rec_keys"}
    mapped = planner._docling_rapid(settings.rapidocr)
    backend = engine.module("docling_backend").DoclingBackend(
        settings.root,
        settings.files,
        mapped,
        settings.smolvlm,
    )
    _, validated = backend._manifest()
    assert set(validated) == {"det", "cls", "rec", "keys"}
