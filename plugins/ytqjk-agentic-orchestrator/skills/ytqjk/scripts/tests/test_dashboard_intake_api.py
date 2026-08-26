from __future__ import annotations

import base64
import hashlib
import http.client
import json
import sys
import threading
from dataclasses import dataclass
from enum import Enum
from pathlib import Path


DASHBOARD = Path(__file__).resolve().parents[2] / "dashboard"
PLUGIN_ROOT = DASHBOARD.parents[2]
sys.path.insert(0, str(DASHBOARD.parent / "scripts"))
sys.path.insert(0, str(DASHBOARD))

from dashboard_intake_api import (  # noqa: E402
    DashboardIntakeApi,
    IntakeApiError,
)
from dashboard_intake_worker import DocumentIntakeWorker  # noqa: E402
from dashboard_documents import snapshot  # noqa: E402
from knowledge_dashboard import KnowledgeHandler  # noqa: E402
from knowledge_engine_locator import locate_knowledge_engine  # noqa: E402


CLEAN_IMAGE = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lE"
    "QVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
)
CLEAN_GIF = base64.b64decode(
    "R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw=="
)


class IdleWorker:
    def __init__(self) -> None:
        self.kicks = 0

    def kick(self) -> None:
        self.kicks += 1


class CandidateState(str, Enum):
    CANDIDATE = "CANDIDATE"


@dataclass(frozen=True)
class Metadata:
    title: str
    summary: str
    pages: tuple[dict[str, object], ...]
    blocks: tuple[dict[str, object], ...]
    review_reasons: tuple[str, ...]


@dataclass(frozen=True)
class Candidate:
    candidate_id: str
    source_digest: str
    state: CandidateState
    metadata: Metadata
    chunks: tuple[dict[str, object], ...]


def candidate_plan() -> Candidate:
    box = {
        "x": 1.0, "y": 2.0, "width": 30.0, "height": 10.0,
        "unit": "PIXELS",
    }
    classification = {
        "category": "diagram", "tags": ["diagram"],
        "summary": "流程图", "confidence": 0.93,
    }
    block = {
        "block_id": "b1", "page_number": 1,
        "bounding_box": box, "confidence": 0.94,
        "image_classification": classification,
    }
    chunk = {
        "id": "c1", "text": "开始 -> 结束", "confidence": 0.94,
        "locator": {"page_number": 1, "bounding_box": box},
    }
    metadata = Metadata(
        "流程图", "识别到流程", ({"number": 1},), (block,),
        ("MANUAL_REVIEW_REQUIRED",),
    )
    digest = hashlib.sha256(CLEAN_IMAGE).hexdigest()
    return Candidate(
        "a" * 64, digest, CandidateState.CANDIDATE, metadata, (chunk,)
    )


def payload(name: str = "diagram.png", source: bytes = CLEAN_IMAGE) -> dict:
    return {
        "name": name,
        "content": base64.b64encode(source).decode("ascii"),
        "encoding": "base64",
        "purpose": "识别流程图",
    }


def api(root: Path) -> tuple[DashboardIntakeApi, IdleWorker]:
    idle = IdleWorker()
    return DashboardIntakeApi(root, PLUGIN_ROOT, worker=idle), idle


def test_submit_persists_safe_relative_job_and_returns_queryable_state(
    tmp_path: Path,
) -> None:
    service, idle = api(tmp_path)
    response = service.submit(payload())
    job_id = response["job"]["id"]
    stored = service.store.get(job_id)

    assert response["job"]["state"] == "QUEUED"
    assert response["job"]["progress"] == 0
    assert idle.kicks == 1
    assert not Path(stored.payload["staging_ref"]).is_absolute()
    assert stored.payload["source_sha256"] in stored.payload["staging_ref"]
    assert set(stored.payload) == {"staging_ref", "source_sha256"}
    assert str(tmp_path) not in json.dumps(response, ensure_ascii=False)
    queried = service.get(f"/api/intake/jobs/{job_id}")
    assert queried["job"]["id"] == job_id


def test_gif_uses_structured_image_queue(tmp_path: Path) -> None:
    service, idle = api(tmp_path)

    assert service.structured_name({"name": "diagram.gif"})
    response = service.submit(payload("diagram.gif", CLEAN_GIF))
    stored = service.store.get(response["job"]["id"])

    assert stored.config["media_type"] == "image"
    assert stored.config["source_name"] == "diagram.gif"
    assert idle.kicks == 1


def test_missing_models_is_explicit_retryable_not_configured(
    tmp_path: Path,
) -> None:
    service, _ = api(tmp_path)
    job_id = service.submit(payload())["job"]["id"]
    engine = locate_knowledge_engine(PLUGIN_ROOT)
    worker = DocumentIntakeWorker(tmp_path, engine, service.store)

    assert worker.process_one()
    failed = service.get(f"/api/intake/jobs/{job_id}")["job"]
    assert failed["state"] == "FAILED"
    assert failed["result"]["status"] == "NOT_CONFIGURED"
    assert failed["result"]["retryable"] is True
    assert failed["result"]["attempt"] == 1
    assert failed["error"]["category"] == "TRANSIENT"
    assert failed["error"]["retryable"] is True
    staged = tmp_path / service.store.get(job_id).payload["staging_ref"]
    assert staged.is_file()
    status, retried = service.action(f"/api/intake/jobs/{job_id}/retry")
    assert status == 202
    assert retried["job"]["state"] == "QUEUED"
    status, cancelled = service.action(f"/api/intake/jobs/{job_id}/cancel")
    assert status == 200
    assert cancelled["job"]["state"] == "CANCELLED"
    assert not staged.exists()
    assert not list(tmp_path.rglob("*.md"))


def test_worker_writes_candidate_with_structured_query_result(
    tmp_path: Path,
) -> None:
    service, _ = api(tmp_path)
    job_id = service.submit(payload())["job"]["id"]
    engine = locate_knowledge_engine(PLUGIN_ROOT)
    worker = DocumentIntakeWorker(
        tmp_path, engine, service.store,
        planner=lambda *_args: candidate_plan(),
    )

    assert worker.process_one()
    job = service.get(f"/api/intake/jobs/{job_id}")["job"]
    result = job["result"]
    candidate = result["candidate"]

    assert job["state"] == "SUCCEEDED"
    assert job["stage"] == "complete"
    assert job["progress"] == 100
    assert job["page_count"] == 1
    assert result["status"] == "CANDIDATE"
    assert candidate["state"] == "CANDIDATE"
    assert candidate["metadata"]["blocks"][0]["confidence"] == 0.94
    assert candidate["metadata"]["blocks"][0][
        "image_classification"
    ]["category"] == "diagram"
    assert candidate["chunks"][0]["locator"]["page_number"] == 1
    document = tmp_path / result["candidate_path"]
    detail = tmp_path / result["detail_path"]
    original = tmp_path / result["original_path"]
    markdown = document.read_text(encoding="utf-8")
    assert "status: CANDIDATE" in markdown
    assert f"intake_id: {job_id}" in markdown
    assert f"original_path: {result['original_path']}" in markdown
    assert f"detail_path: {result['detail_path']}" in markdown
    assert f"source_sha256: {result['source_sha256']}" in markdown
    assert original.read_bytes() == CLEAN_IMAGE
    assert hashlib.sha256(original.read_bytes()).hexdigest() == (
        result["source_sha256"]
    )
    assert json.loads(detail.read_text(encoding="utf-8")) == candidate
    chunk_paths = [tmp_path / value for value in result["chunk_paths"]]
    assert len(chunk_paths) == 1
    chunk_content = chunk_paths[0].read_text(encoding="utf-8")
    assert "status: CANDIDATE" in chunk_content
    assert "开始 -> 结束" in chunk_content
    assert '"page_number": 1' in chunk_content
    dashboard = snapshot(tmp_path)
    logical_candidates = [
        item for item in dashboard["documents"]
        if item["state"] == "candidate"
    ]
    assert dashboard["counts"]["candidate"] == 1
    assert [item["path"] for item in logical_candidates] == [
        result["candidate_path"]
    ]
    assert str(tmp_path) not in json.dumps(result, ensure_ascii=False)
    staged = tmp_path / service.store.get(job_id).payload["staging_ref"]
    assert not staged.exists()
    repeated = service.submit(payload())
    assert repeated["job"]["id"] == job_id
    assert not staged.exists()
    assert not list(tmp_path.rglob("approved/*.md"))


def test_sensitive_content_and_filename_never_enqueue(
    tmp_path: Path,
) -> None:
    service, _ = api(tmp_path)
    secret = b"api_key=ABCDEFGHIJKLMNOPQRSTUV"
    try:
        service.submit(payload(source=secret))
    except IntakeApiError as error:
        assert error.status == 400
    else:
        raise AssertionError("secret upload was accepted")
    try:
        service.submit(payload(name="token.json"))
    except IntakeApiError as error:
        assert error.status == 400
    else:
        raise AssertionError("unsupported sensitive name was accepted")
    assert service.store.list() == ()


def test_http_jobs_require_loopback_host_same_origin_and_json(
    tmp_path: Path,
) -> None:
    service, _ = api(tmp_path)
    handler = type(
        "IntakeHandler", (KnowledgeHandler,), {
            "knowledge_root": tmp_path,
            "plugin_root": PLUGIN_ROOT,
            "update_lock": threading.Lock(),
            "update_token": "test-token",
            "schedule_restart": staticmethod(lambda: None),
            "intake_api": service,
        },
    )
    from http.server import ThreadingHTTPServer

    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    port = server.server_address[1]
    body = json.dumps(payload()).encode("utf-8")
    good = {
        "Host": f"127.0.0.1:{port}",
        "Origin": f"http://127.0.0.1:{port}",
        "Content-Type": "application/json",
    }
    try:
        connection = http.client.HTTPConnection("127.0.0.1", port)
        connection.request("POST", "/api/intake", body, good)
        response = connection.getresponse()
        created = json.loads(response.read())
        assert response.status == 202
        job_id = created["job"]["id"]
        assert {
            "id", "state", "stage", "progress", "page_count",
            "attempt", "revision", "created_at", "updated_at",
        } <= set(created["job"])

        engine = locate_knowledge_engine(PLUGIN_ROOT)
        worker = DocumentIntakeWorker(tmp_path, engine, service.store)
        assert worker.process_one()
        for action, expected in (("retry", 202), ("cancel", 200)):
            connection.request(
                "POST",
                f"/api/intake/jobs/{job_id}/{action}",
                b"{}",
                good,
            )
            changed = connection.getresponse()
            changed.read()
            assert changed.status == expected

        connection.request(
            "GET", f"/api/intake/jobs/{job_id}",
            headers={"Host": f"evil.test:{port}"},
        )
        denied = connection.getresponse()
        denied.read()
        assert denied.status == 403

        bad = dict(good)
        bad["Origin"] = "http://evil.test"
        connection.request("POST", "/api/intake", body, bad)
        denied = connection.getresponse()
        denied.read()
        assert denied.status == 403

        missing_origin = dict(good)
        missing_origin.pop("Origin")
        wrong_type = dict(good)
        wrong_type["Content-Type"] = "text/plain"
        alias_origin = dict(good)
        alias_origin["Host"] = f"localhost:{port}"
        for headers in (missing_origin, wrong_type, alias_origin):
            connection.request("POST", "/api/intake", body, headers)
            denied = connection.getresponse()
            denied.read()
            assert denied.status == 403
        assert len(service.store.list()) == 1
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
