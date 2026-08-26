from __future__ import annotations

import http.client
import json
import sys
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_peer_client import KnowledgePeerClient  # noqa: E402
from knowledge_peer_contract import signed_headers  # noqa: E402
from peer_test_support import (  # noqa: E402
    PROJECT_ID,
    pair,
    start_server,
    write_project,
)


def test_two_peers_query_and_fetch_material(tmp_path: Path) -> None:
    client_root = tmp_path / "client"
    server_root = tmp_path / "server"
    write_project(server_root, "LAN_UNIQUE_MARKER")
    server, thread = start_server(server_root)
    try:
        endpoint = f"http://127.0.0.1:{server.server_port}"
        _, server_id, _ = pair(client_root, server_root, endpoint)
        client = KnowledgePeerClient(client_root)

        result = client.query(
            server_id, PROJECT_ID, "LAN_UNIQUE_MARKER", 5
        )
        assert result["status"] == "PEER_HIT"
        assert result["project_id"] == PROJECT_ID
        assert result["results"][0]["content"] == "LAN_UNIQUE_MARKER"
        material = client.material(
            server_id,
            PROJECT_ID,
            result["results"][0]["material_id"],
        )
        assert material["content"] == "LAN_UNIQUE_MARKER"
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def test_server_rejects_replay_and_cross_project(tmp_path: Path) -> None:
    client_root = tmp_path / "client"
    server_root = tmp_path / "server"
    write_project(server_root, "LAN_MARKER")
    server, thread = start_server(server_root)
    try:
        endpoint = f"http://127.0.0.1:{server.server_port}"
        client_id, _, secret = pair(
            client_root, server_root, endpoint
        )
        body = json.dumps(
            {
                "project_id": PROJECT_ID,
                "node_id": PROJECT_ID,
                "query": "LAN_MARKER",
                "limit": 5,
            },
            separators=(",", ":"),
            sort_keys=True,
        ).encode("utf-8")
        headers = signed_headers(
            client_id,
            secret,
            "POST",
            "/v1/query",
            body,
            nonce="R" * 22,
        )
        headers["Content-Type"] = "application/json"
        first = _post(server.server_port, "/v1/query", body, headers)
        second = _post(server.server_port, "/v1/query", body, headers)
        assert first[0] == 200
        assert second == (401, "PEER_REPLAY_REJECTED")

        foreign = body.replace(
            PROJECT_ID.encode("utf-8"), b"foreign--0123456789ab"
        )
        foreign_headers = signed_headers(
            client_id,
            secret,
            "POST",
            "/v1/query",
            foreign,
            nonce="S" * 22,
        )
        foreign_headers["Content-Type"] = "application/json"
        rejected = _post(
            server.server_port,
            "/v1/query",
            foreign,
            foreign_headers,
        )
        assert rejected == (401, "PEER_PROJECT_FORBIDDEN")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def _post(
    port: int,
    path: str,
    body: bytes,
    headers: dict[str, str],
) -> tuple[int, str]:
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        connection.request("POST", path, body=body, headers=headers)
        response = connection.getresponse()
        payload = json.loads(response.read().decode("utf-8"))
        return response.status, str(payload.get("error", ""))
    finally:
        connection.close()
