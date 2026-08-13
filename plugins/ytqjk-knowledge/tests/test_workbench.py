from __future__ import annotations

import http.client
import json
import re
import sys
import threading
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
WORKBENCH = SKILL_ROOT / "workbench"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.service import KnowledgeService  # noqa: E402
from workbench.app import WorkbenchContext, WorkbenchServer  # noqa: E402


@pytest.fixture
def server(tmp_path: Path):
    service = KnowledgeService(tmp_path / "workbench.sqlite3")
    project_id = service.create_project("project", "workbench")
    context = WorkbenchContext(service, project_id, "token")
    server = WorkbenchServer(("127.0.0.1", 0), context)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server, service, project_id
    finally:
        server.shutdown()
        thread.join()
        server.server_close()


def request(
    server: WorkbenchServer,
    method: str,
    path: str,
    body: dict | None = None,
    headers: dict | None = None,
):
    connection = http.client.HTTPConnection("127.0.0.1", server.server_port)
    combined = headers or {}
    payload = json.dumps(body) if body is not None else None
    connection.request(method, path, payload, combined)
    response = connection.getresponse()
    data = response.read()
    connection.close()
    return response.status, data


def write_headers(server: WorkbenchServer) -> dict[str, str]:
    origin = f"http://127.0.0.1:{server.server_port}"
    return {
        "Host": origin.removeprefix("http://"),
        "Origin": origin,
        "Content-Type": "application/json",
        "X-CSRF-Token": "token",
    }


def test_loopback_only_and_static_assets(server) -> None:
    current, _, _ = server
    assert current.server_address[0] == "127.0.0.1"
    with pytest.raises(ValueError, match="127.0.0.1"):
        WorkbenchServer(("0.0.0.0", 0), current.context)
    for path in ("/", "/app.css", "/app.js"):
        status, body = request(current, "GET", path)
        assert status == 200 and body
    status, body = request(
        current, "GET", "/api/state", headers={"Host": "example.test"}
    )
    assert status == 403 and b"csrf_token" not in body


def test_pending_candidate_selector_matches_html_id() -> None:
    html = (WORKBENCH / "static" / "index.html").read_text(encoding="utf-8")
    script = (WORKBENCH / "static" / "app.js").read_text(encoding="utf-8")
    match = re.search(r'<ul id="([^"]+)" aria-live="polite">', html)
    assert match is not None
    assert f"querySelector('#{match.group(1)}')" in script


def test_state_keeps_one_snapshot_generation_and_escapes_data(server) -> None:
    current, service, project_id = server
    document = service.create_candidate(
        project_id, "<script>alert(1)</script>", "draft", "manual"
    )
    service.create_snapshot(project_id)
    status, body = request(current, "GET", "/api/state")
    data = json.loads(body)
    assert status == 200
    assert data["snapshot"]["generation"] == 1
    assert data["snapshot_documents"][0]["id"] == document
    assert data["project_pending_candidates"]["items"] == []
    assert data["retrieval"]["state"] == "NOT_CONFIGURED"
    status, page = request(current, "GET", "/")
    assert status == 200 and b"<script>alert(1)</script>" not in page


def test_write_requires_local_origin_and_csrf(server) -> None:
    current, service, _ = server
    body = {"title": "note", "content": "draft", "source": "manual"}
    status, _ = request(current, "POST", "/api/candidates", body)
    assert status == 403 and service.count("documents") == 0
    status, _ = request(
        current,
        "POST",
        "/api/candidates",
        body,
        {"Host": "example.test", "X-CSRF-Token": "token"},
    )
    assert status == 403 and service.count("documents") == 0
    headers = write_headers(current)
    headers.pop("X-CSRF-Token")
    status, response = request(
        current, "POST", "/api/candidates", body, headers
    )
    assert status == 403 and b"CSRF_REQUIRED" in response
    headers = write_headers(current)
    headers["Origin"] = "http://example.test"
    status, _ = request(current, "POST", "/api/candidates", body, headers)
    assert status == 403 and service.count("documents") == 0
    status, _ = request(
        current, "POST", "/api/candidates", body, write_headers(current)
    )
    assert status == 200 and service.count("documents") == 1


def test_created_candidate_is_process_local_pending_and_actionable(
    server,
) -> None:
    current, service, project_id = server
    headers = write_headers(current)
    status, body = request(
        current,
        "POST",
        "/api/candidates",
        {"title": "note", "content": "draft", "source": "manual"},
        headers,
    )
    document = json.loads(body)["document_id"]
    status, body = request(current, "GET", "/api/state")
    state = json.loads(body)
    assert status == 200
    assert state["snapshot"]["generation"] is None
    assert state["snapshot_documents"] == []
    pending = state["project_pending_candidates"]
    assert pending["items"][0]["id"] == document
    assert pending["restart_recovery"] == "NOT_CONFIGURED"
    other = service.create_project("project", "hidden")
    service.create_candidate(other, "other", "draft", "manual")
    assert all(item["id"] != other for item in pending["items"])
    status, _ = request(
        current,
        "POST",
        "/api/candidates/edit",
        {"document_id": document, "content": "next", "source": "manual"},
        headers,
    )
    assert status == 200


def test_deleted_pending_candidate_disappears_after_success(server) -> None:
    current, _, _ = server
    headers = write_headers(current)
    ids = []
    for title in ("one", "two"):
        status, body = request(
            current,
            "POST",
            "/api/candidates",
            {"title": title, "content": "draft", "source": "manual"},
            headers,
        )
        assert status == 200
        ids.append(json.loads(body)["document_id"])
    status, _ = request(
        current,
        "POST",
        "/api/candidates/delete",
        {"document_id": ids[0]},
        headers,
    )
    assert status == 200
    status, body = request(current, "GET", "/api/state")
    pending = json.loads(body)["project_pending_candidates"]["items"]
    assert status == 200 and [item["id"] for item in pending] == [ids[1]]
    assert ids[0] not in current.created_documents
    assert ids[1] in current.created_documents


def test_candidate_actions_and_approval_fail_closed_without_secret_echo(
    server,
) -> None:
    current, service, project_id = server
    headers = write_headers(current)
    document = service.create_candidate(project_id, "note", "draft", "manual")
    service.create_snapshot(project_id)
    status, _ = request(
        current,
        "POST",
        "/api/candidates/edit",
        {"document_id": document, "content": "next", "source": "manual"},
        headers,
    )
    assert status == 200
    status, body = request(
        current,
        "POST",
        "/api/candidates/approve",
        {"document_id": document},
        headers,
    )
    assert status == 409
    assert json.loads(body)["status"] == "NOT_CONFIGURED"
    assert service.document_versions(document)[-1]["state"] == "candidate"
    status, body = request(
        current,
        "POST",
        "/api/candidates/delete",
        {"document_id": document},
        headers,
    )
    assert status == 200 and str(project_id).encode() not in body


def test_cross_project_and_unknown_candidates_fail_closed(server) -> None:
    current, service, project_id = server
    other_project = service.create_project("project", "other")
    other = service.create_candidate(other_project, "note", "draft", "manual")
    service.create_snapshot(other_project)
    service.create_snapshot(project_id)
    headers = write_headers(current)
    for path in ("edit", "delete", "approve"):
        payload = {"document_id": other}
        if path == "edit":
            payload.update({"content": "changed", "source": "manual"})
        status, _ = request(
            current, "POST", f"/api/candidates/{path}", payload, headers
        )
        assert status == 400
    unknown = "00000000-0000-0000-0000-000000000099"
    for path in ("edit", "delete", "approve"):
        payload = {"document_id": unknown}
        if path == "edit":
            payload.update({"content": "changed", "source": "manual"})
        status, _ = request(
            current, "POST", f"/api/candidates/{path}", payload, headers
        )
        assert status == 400
    assert service.document_versions(other)[-1]["state"] == "candidate"
    assert current.created_documents == set()
