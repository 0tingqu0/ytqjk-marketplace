from __future__ import annotations

import http.client
import json
import sys
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_peer_contract import signed_headers  # noqa: E402
from peer_test_support import (  # noqa: E402
    PROJECT_ID,
    pair,
    start_server,
    write_project,
)


def test_health_fails_when_export_scope_is_invalid(
    tmp_path: Path,
) -> None:
    client_root = tmp_path / "client"
    server_root = tmp_path / "server"
    write_project(server_root, "MARKER")
    server, thread = start_server(server_root)
    try:
        endpoint = f"http://127.0.0.1:{server.server_port}"
        client_id, _, secret = pair(
            client_root,
            server_root,
            endpoint,
            remote_node_id="missing-export",
            server_export_node_id="missing-export",
        )
        body = json.dumps(
            {"project_id": PROJECT_ID},
            separators=(",", ":"),
            sort_keys=True,
        ).encode("utf-8")
        headers = signed_headers(
            client_id, secret, "POST", "/v1/health", body
        )
        headers["Content-Type"] = "application/json"
        connection = http.client.HTTPConnection(
            "127.0.0.1", server.server_port, timeout=5
        )
        try:
            connection.request(
                "POST", "/v1/health", body=body, headers=headers
            )
            response = connection.getresponse()
            payload = json.loads(response.read().decode("utf-8"))
        finally:
            connection.close()
        assert response.status == 400
        assert payload["error"] == "PEER_EXPORT_NODE_MISSING"
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
