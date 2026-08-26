from __future__ import annotations

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

from knowledge_dashboard import KnowledgeHandler  # noqa: E402
from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402


_COMMIT_PATHS = {
    "attach": "/api/libraries/attach",
    "create": "/api/libraries/create",
    "detach": "/api/libraries/detach",
    "insert_between": "/api/libraries/insert-between",
    "move": "/api/libraries/move",
}


def _node(node_id: str, kind: str = "group") -> LibraryNode:
    return LibraryNode(node_id, f"Node {node_id}", kind)


def _initial_tree() -> KnowledgeTree:
    return KnowledgeTree(
        (
            _node("global", "global"),
            _node("alpha", "project"),
            _node("leaf"),
            _node("other"),
            _node("bridge"),
            _node("orphan"),
        ),
        (("global", "alpha"), ("alpha", "leaf")),
    )


class Client:
    def __init__(self, port: int) -> None:
        self.port = port

    def get(
        self,
        path: str = "/api/libraries/tree",
        headers: dict[str, str] | None = None,
    ) -> tuple[int, dict[str, object]]:
        base = {"Host": f"127.0.0.1:{self.port}"}
        return self._request("GET", path, None, headers or base)

    def post(
        self,
        path: str,
        payload: object,
        headers: dict[str, str] | None = None,
    ) -> tuple[int, dict[str, object]]:
        base = {
            "Host": f"127.0.0.1:{self.port}",
            "Origin": f"http://127.0.0.1:{self.port}",
            "Content-Type": "application/json",
        }
        return self._request("POST", path, payload, headers or base)

    def preview(
        self,
        action: str,
        arguments: object,
    ) -> tuple[int, dict[str, object]]:
        return self.post(
            "/api/libraries/preview",
            {"action": action, "payload": arguments},
        )

    def preview_commit(
        self,
        action: str,
        arguments: dict[str, object],
    ) -> dict[str, object]:
        status, body = self.preview(action, arguments)
        assert status == 200
        issued = body["preview"]
        status, result = self.post(
            _COMMIT_PATHS[action],
            {
                "digest": issued["digest"],
                "expected_revision": issued["expected_revision"],
            },
        )
        assert status == 200
        return result

    def _request(
        self,
        method: str,
        path: str,
        payload: object | None,
        headers: dict[str, str],
    ) -> tuple[int, dict[str, object]]:
        content = None
        if payload is not None:
            content = json.dumps(payload).encode("utf-8")
        connection = http.client.HTTPConnection(
            "127.0.0.1", self.port, timeout=5,
        )
        try:
            connection.request(method, path, content, headers)
            response = connection.getresponse()
            body = json.loads(response.read().decode("utf-8"))
            return response.status, body
        finally:
            connection.close()


@contextmanager
def _server(tmp_path: Path) -> Iterator[Client]:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    store.save(_initial_tree(), expected_revision=-1)
    handler = type(
        "TreeHttpHandler",
        (KnowledgeHandler,),
        {
            "knowledge_root": tmp_path,
            "plugin_root": ROOT.parents[1],
            "update_lock": threading.Lock(),
            "update_token": "test-token",
            "schedule_restart": staticmethod(lambda: None),
            "intake_api": None,
            "tree_api": None,
        },
    )
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield Client(int(server.server_address[1]))
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)


def _nodes(body: dict[str, object]) -> dict[str, dict[str, object]]:
    return {
        node["id"]: node
        for node in body["tree"]["nodes"]
    }


def test_get_and_all_five_preview_commit_routes(tmp_path: Path) -> None:
    with _server(tmp_path) as client:
        status, _ = client.get()
        assert status == 200

        client.preview_commit(
            "create",
            {
                "node_id": "team", "title": "Team",
                "type": "group", "parent_id": None, "metadata": {},
            },
        )
        client.preview_commit(
            "attach", {"node_id": "team", "parent_id": "global"},
        )
        client.preview_commit(
            "detach", {"node_id": "alpha"},
        )
        client.preview_commit(
            "move", {"node_id": "leaf", "parent_id": "team"},
        )
        result = client.preview_commit(
            "insert_between",
            {
                "parent_id": "global", "node_id": "team",
                "middle_id": "bridge",
            },
        )
        assert result["revision"] == 5

        status, final = client.get()
        nodes = _nodes(final)
        assert status == 200
        assert nodes["bridge"]["parent_id"] == "global"
        assert nodes["team"]["parent_id"] == "bridge"
        assert nodes["leaf"]["parent_id"] == "team"
        assert nodes["alpha"]["parent_id"] is None


def test_host_origin_and_content_type_guards_do_not_mutate_tree(
    tmp_path: Path,
) -> None:
    with _server(tmp_path) as client:
        evil_host = {"Host": f"evil.test:{client.port}"}
        status, _ = client.get(headers=evil_host)
        assert status == 403

        good_host = f"127.0.0.1:{client.port}"
        good_origin = f"http://127.0.0.1:{client.port}"
        denied = (
            {
                "Host": f"evil.test:{client.port}",
                "Origin": f"http://evil.test:{client.port}",
                "Content-Type": "application/json",
            },
            {"Host": good_host, "Content-Type": "application/json"},
            {
                "Host": good_host, "Origin": "http://evil.test",
                "Content-Type": "application/json",
            },
            {
                "Host": good_host, "Origin": good_origin,
                "Content-Type": "text/plain",
            },
        )
        for headers in denied:
            status, _ = client.post(
                "/api/libraries/preview",
                {
                    "action": "detach",
                    "payload": {"node_id": "alpha"},
                },
                headers,
            )
            assert status == 403
        status, body = client.get()
        assert status == 200
        assert body["tree"]["revision"] == 0


def test_invalid_fields_unknown_node_and_cycle_do_not_mutate(
    tmp_path: Path,
) -> None:
    with _server(tmp_path) as client:
        cases = (
            (
                {
                    "action": "detach",
                    "payload": {"node_id": "alpha"},
                    "extra": True,
                },
                400,
                "INVALID_REQUEST_FIELDS",
            ),
            (
                {
                    "action": "detach",
                    "payload": {"node_id": "alpha", "extra": True},
                },
                400,
                "INVALID_REQUEST_FIELDS",
            ),
            (
                {
                    "action": "detach",
                    "payload": {"node_id": "missing"},
                },
                404,
                "UNKNOWN_NODE",
            ),
            (
                {
                    "action": "attach",
                    "payload": {
                        "node_id": "global", "parent_id": "leaf",
                    },
                },
                409,
                "CYCLE_DETECTED",
            ),
        )
        for payload, expected_status, code in cases:
            status, body = client.post(
                "/api/libraries/preview", payload,
            )
            assert status == expected_status
            assert body["error"]["code"] == code
        status, body = client.get()
        assert status == 200
        assert body["tree"]["revision"] == 0


def test_stale_and_replayed_commits_change_tree_only_once(
    tmp_path: Path,
) -> None:
    with _server(tmp_path) as client:
        status, first_body = client.preview(
            "attach", {"node_id": "orphan", "parent_id": "alpha"},
        )
        assert status == 200
        status, stale_body = client.preview(
            "attach", {"node_id": "bridge", "parent_id": "alpha"},
        )
        assert status == 200
        first = first_body["preview"]
        stale = stale_body["preview"]
        first_commit = {
            "digest": first["digest"],
            "expected_revision": first["expected_revision"],
        }
        stale_commit = {
            "digest": stale["digest"],
            "expected_revision": stale["expected_revision"],
        }

        status, _ = client.post(
            "/api/libraries/attach", first_commit,
        )
        assert status == 200
        status, body = client.post(
            "/api/libraries/attach", stale_commit,
        )
        assert status == 409
        assert body["error"]["code"] == "REVISION_CONFLICT"
        status, body = client.post(
            "/api/libraries/attach", first_commit,
        )
        assert status == 409
        assert body["error"]["code"] == "PREVIEW_NOT_FOUND"

        status, final = client.get()
        nodes = _nodes(final)
        assert status == 200
        assert final["tree"]["revision"] == 1
        assert nodes["orphan"]["parent_id"] == "alpha"
        assert nodes["bridge"]["parent_id"] is None
