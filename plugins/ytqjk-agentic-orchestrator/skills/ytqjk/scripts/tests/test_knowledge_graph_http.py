from __future__ import annotations

import http.client
import json
import sys
import threading
from contextlib import contextmanager
from http.server import ThreadingHTTPServer
from pathlib import Path
from typing import Iterator


SKILL_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SKILL_ROOT / "dashboard"))
sys.path.insert(0, str(SKILL_ROOT / "scripts"))

from knowledge_dashboard import KnowledgeHandler  # noqa: E402


class Client:
    def __init__(self, port: int) -> None:
        self.port = port

    def request(
        self, method: str, path: str, payload: object | None = None,
    ) -> tuple[int, dict[str, object]]:
        headers = {"Host": f"127.0.0.1:{self.port}"}
        body = None
        if payload is not None:
            headers.update({
                "Origin": f"http://127.0.0.1:{self.port}",
                "Content-Type": "application/json",
            })
            body = json.dumps(payload).encode("utf-8")
        connection = http.client.HTTPConnection(
            "127.0.0.1", self.port, timeout=5,
        )
        try:
            connection.request(method, path, body, headers)
            response = connection.getresponse()
            return response.status, json.loads(
                response.read().decode("utf-8")
            )
        finally:
            connection.close()


@contextmanager
def _server(root: Path) -> Iterator[Client]:
    approved = root / "verified" / "graph.md"
    approved.parent.mkdir(parents=True)
    approved.write_text(
        "# 知识图谱\n[[知识图谱]] 使用 [[语义搜索]]。",
        encoding="utf-8",
    )
    handler = type(
        "GraphHttpHandler",
        (KnowledgeHandler,),
        {
            "knowledge_root": root,
            "plugin_root": SKILL_ROOT.parents[1],
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


def test_graph_search_recommendation_and_path_contracts(
    tmp_path: Path,
) -> None:
    with _server(tmp_path) as client:
        status, body = client.request("GET", "/api/knowledge-graph")
        assert status == 200
        assert body["ok"] is True
        assert isinstance(body["revision"], str)
        assert body["graph"]["nodes"]
        status, revision = client.request(
            "GET", "/api/knowledge-graph-revision",
        )
        assert status == 200
        assert revision == {"ok": True, "revision": body["revision"]}
        nodes = body["graph"]["nodes"]
        source = next(node["id"] for node in nodes if node["label"] == "知识图谱")
        target = next(node["id"] for node in nodes if node["label"] == "语义搜索")

        status, search = client.request(
            "POST", "/api/knowledge-search", {"query": "语义", "limit": 5},
        )
        assert status == 200
        assert search["ok"] is True
        assert search["results"]

        status, recommended = client.request(
            "POST", "/api/knowledge-recommendations",
            {"node_id": source, "limit": 5},
        )
        assert status == 200
        assert recommended["ok"] is True

        status, path = client.request(
            "POST", "/api/knowledge-path",
            {"source": source, "target": target, "max_depth": 4},
        )
        assert status == 200
        assert path["found"] is True


def test_invalid_graph_requests_return_structured_errors(
    tmp_path: Path,
) -> None:
    with _server(tmp_path) as client:
        status, body = client.request(
            "POST", "/api/knowledge-search", {"query": "", "limit": 5},
        )
        assert status == 400
        assert body["error"]["code"] == "EMPTY_QUERY"

        status, body = client.request(
            "POST", "/api/knowledge-path",
            {"source": "a", "target": "b", "max_depth": 99},
        )
        assert status == 400
        assert body["error"]["code"] == "INVALID_MAX_DEPTH"
