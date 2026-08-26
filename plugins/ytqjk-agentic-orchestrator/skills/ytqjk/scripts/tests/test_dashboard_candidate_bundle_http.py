from __future__ import annotations

import http.client
import json
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path

from test_structured_candidate_lifecycle import (
    DASHBOARD,
    _create_bundle,
    _relative,
)

from knowledge_dashboard import KnowledgeHandler  # noqa: E402


def test_internal_bundle_members_are_not_candidate_api_resources(
    tmp_path: Path,
) -> None:
    bundle = _create_bundle(tmp_path)
    handler = type(
        "CandidateHandler",
        (KnowledgeHandler,),
        {
            "knowledge_root": tmp_path,
            "plugin_root": DASHBOARD.parents[2],
            "update_lock": threading.Lock(),
            "update_token": "test-token",
            "schedule_restart": staticmethod(lambda: None),
        },
    )
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    port = server.server_address[1]
    headers = {
        "Host": f"127.0.0.1:{port}",
        "Origin": f"http://127.0.0.1:{port}",
        "Content-Type": "application/json",
    }
    connection = http.client.HTTPConnection("127.0.0.1", port)
    try:
        for artifact_name in ("chunk", "original"):
            artifact = bundle[artifact_name]
            assert isinstance(artifact, Path)
            raw_path = _relative(tmp_path, artifact)
            requests = (
                (
                    "PUT",
                    "/api/candidate",
                    {"path": raw_path, "content": "forbidden",
                     "expected_version": "0" * 64},
                ),
                ("DELETE", "/api/candidate", {"path": raw_path}),
                (
                    "POST",
                    "/api/candidate/approve",
                    {"path": raw_path},
                ),
            )
            for method, endpoint, payload in requests:
                connection.request(
                    method,
                    endpoint,
                    json.dumps(payload).encode("utf-8"),
                    headers,
                )
                response = connection.getresponse()
                result = json.loads(response.read())
                assert response.status == 400
                assert result["ok"] is False
                assert artifact.exists()
    finally:
        connection.close()
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
