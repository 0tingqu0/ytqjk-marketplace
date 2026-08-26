from __future__ import annotations

import hashlib
import http.client
import json
import sys
import threading
from contextlib import contextmanager
from http.server import ThreadingHTTPServer
from pathlib import Path
from typing import Iterator


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "dashboard"))
sys.path.insert(0, str(ROOT / "scripts"))

from global_session_query import query_global  # noqa: E402
from knowledge_dashboard import KnowledgeHandler  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402
from project_tracking import identify_project, track_project  # noqa: E402


class Client:
    def __init__(self, port: int) -> None:
        self.port = port

    def get(self) -> tuple[int, dict[str, object]]:
        return self.request("GET", "/api/libraries/tree", None)

    def post(
        self,
        path: str,
        payload: object,
        headers: dict[str, str] | None = None,
    ) -> tuple[int, dict[str, object]]:
        return self.request("POST", path, payload, headers)

    def mutate(
        self,
        action: str,
        payload: dict[str, object],
    ) -> tuple[int, dict[str, object], dict[str, object]]:
        status, preview = self.post(
            "/api/libraries/preview",
            {"action": action, "payload": payload},
        )
        assert status == 200
        issued = preview["preview"]
        status, result = self.post(
            f"/api/libraries/{action.replace('_', '-')}",
            {
                "digest": issued["digest"],
                "expected_revision": issued["expected_revision"],
            },
        )
        return status, result, issued

    def request(
        self,
        method: str,
        path: str,
        payload: object | None,
        headers: dict[str, str] | None = None,
    ) -> tuple[int, dict[str, object]]:
        base = {
            "Host": f"127.0.0.1:{self.port}",
            "Origin": f"http://127.0.0.1:{self.port}",
            "Content-Type": "application/json",
        }
        connection = http.client.HTTPConnection(
            "127.0.0.1", self.port, timeout=5,
        )
        content = None
        if payload is not None:
            content = json.dumps(payload).encode("utf-8")
        try:
            connection.request(
                method, path, content, headers or base
            )
            response = connection.getresponse()
            body = json.loads(response.read().decode("utf-8"))
            return response.status, body
        finally:
            connection.close()


@contextmanager
def _server(
    knowledge: Path,
) -> Iterator[Client]:
    handler = type(
        "MaterializationHandler",
        (KnowledgeHandler,),
        {
            "knowledge_root": knowledge,
            "plugin_root": ROOT.parents[1],
            "update_lock": threading.Lock(),
            "update_token": "test-token",
            "schedule_restart": staticmethod(lambda: None),
            "intake_api": None,
            "tree_api": None,
        },
    )
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(
        target=server.serve_forever,
        daemon=True,
    )
    thread.start()
    try:
        yield Client(int(server.server_address[1]))
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)


def _node(body: dict[str, object], node_id: str) -> dict[str, object]:
    return next(
        item for item in body["tree"]["nodes"]
        if item["id"] == node_id
    )


def test_http_create_materialize_query_and_drift(
    tmp_path: Path,
) -> None:
    project = tmp_path / "project"
    project.mkdir()
    project_id = identify_project(project)["id"]
    knowledge = tmp_path / "knowledge"
    track_project(knowledge, project)
    KnowledgeTreeStore(knowledge / "tree.json").bootstrap(
        knowledge / "catalog.json"
    )
    source = knowledge / "verified" / "fact.md"
    source.parent.mkdir(parents=True)
    source.write_text("GROUP_QUERY_MARKER", encoding="utf-8")

    with _server(knowledge) as client:
        status, _, _ = client.mutate(
            "create",
            {
                "node_id": "team",
                "title": "Team",
                "type": "group",
                "parent_id": "global",
                "metadata": {},
            },
        )
        assert status == 200
        status, _, _ = client.mutate(
            "move",
            {"node_id": project_id, "parent_id": "team"},
        )
        assert status == 200
        status, rebuilt, issued = client.mutate(
            "rebuild_index",
            {"node_id": "team", "document_ids": []},
        )
        assert status == 200
        assert rebuilt["materialization"]["status"] == "REBUILT"
        assert _node(rebuilt, "team")["index"]["status"] == "READY"

        result = query_global(
            knowledge,
            project,
            "GROUP_QUERY_MARKER",
            "session-materialized",
            project_id,
            5,
        )
        assert result["hit_node"] == "team"
        assert result["prefetch_count"] == 1

        source.write_text("SOURCE_CHANGED", encoding="utf-8")
        stale = query_global(
            knowledge,
            project,
            "GROUP_QUERY_MARKER",
            "session-materialized",
            project_id,
            5,
        )
        assert stale["status"] == "KNOWLEDGE_MISS"
        status, tree = client.get()
        assert status == 200
        assert _node(tree, "team")["index"]["status"] == "STALE"

        replay_status, replay = client.post(
            "/api/libraries/rebuild-index",
            {
                "digest": issued["digest"],
                "expected_revision": issued["expected_revision"],
            },
        )
        assert replay_status == 409
        assert replay["error"]["code"] == "PREVIEW_NOT_FOUND"


def test_rebuild_guards_and_ui_contract(tmp_path: Path) -> None:
    knowledge = tmp_path / "knowledge"
    knowledge.mkdir()
    KnowledgeTreeStore(knowledge / "tree.json").bootstrap(
        {"projects": {}}
    )
    with _server(knowledge) as client:
        status, created, _ = client.mutate(
            "create",
            {
                "node_id": "team",
                "title": "Team",
                "type": "group",
                "parent_id": "global",
                "metadata": {},
            },
        )
        assert status == 200
        revision = created["tree"]["revision"]
        evil = {
            "Host": f"127.0.0.1:{client.port}",
            "Origin": "http://evil.test",
            "Content-Type": "application/json",
        }
        denied, _ = client.post(
            "/api/libraries/preview",
            {
                "action": "rebuild_index",
                "payload": {
                    "node_id": "team",
                    "document_ids": [],
                },
            },
            evil,
        )
        assert denied == 403
        status, body = client.post(
            "/api/libraries/preview",
            {
                "action": "rebuild_index",
                "payload": {
                    "node_id": "team",
                    "document_ids": [],
                    "path": "verified/fact.md",
                },
            },
        )
        assert status == 400
        assert body["error"]["code"] == "INVALID_REQUEST_FIELDS"
        unknown_id = hashlib.sha256(b"candidate").hexdigest()
        status, preview = client.post(
            "/api/libraries/preview",
            {
                "action": "rebuild_index",
                "payload": {
                    "node_id": "team",
                    "document_ids": [unknown_id],
                },
            },
        )
        assert status == 200
        issued = preview["preview"]
        status, failed = client.post(
            "/api/libraries/rebuild-index",
            {
                "digest": issued["digest"],
                "expected_revision": issued["expected_revision"],
            },
        )
        assert status == 400
        assert failed["error"]["code"] == "UNKNOWN_DOCUMENT_ID"
        _, current = client.get()
        assert current["tree"]["revision"] == revision
        assert _node(current, "team")["index"]["status"] == (
            "NOT_CONFIGURED"
        )

    ui = (
        ROOT / "dashboard" / "js" / "views" / "libraries.js"
    ).read_text(encoding="utf-8")
    assert "重建索引" in ui
    assert 'treePreview("rebuild_index"' in ui
