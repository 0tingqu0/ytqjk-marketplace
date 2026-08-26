from __future__ import annotations

import hashlib
import http.client
import json
import socket
import sys
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_peer_client import KnowledgePeerClient  # noqa: E402
from knowledge_peer_codec import PeerStoreError, validate_local  # noqa: E402
from knowledge_peer_contract import (  # noqa: E402
    PeerContractError,
    PeerRecord,
    endpoint,
    new_secret,
    signed_headers,
)
from knowledge_peer_server import start_peer_server  # noqa: E402
from knowledge_peer_store import PeerConfigStore  # noqa: E402
from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402
from peer_test_support import write_project  # noqa: E402
from rag_common import (  # noqa: E402
    Chunk,
    SCHEMA_VERSION,
    atomic_json,
    build_lexical,
)


PROJECT_ID = "shared-runtime-project--0123456789ab"
NODE_A = "runtime-node-a"
NODE_B = "runtime-node-b"


def test_formal_servers_exchange_distinct_subtrees_and_reject_replay(
    tmp_path: Path,
) -> None:
    root_a = tmp_path / "knowledge-a"
    root_b = tmp_path / "knowledge-b"
    material_a = _write_root(root_a, NODE_A, "RUNTIME_MARKER_A")
    material_b = _write_root(root_b, NODE_B, "RUNTIME_MARKER_B")
    port_a = _free_port()
    port_b = _free_port()
    local_a, local_b, secret = _pair(
        root_a,
        root_b,
        port_a,
        port_b,
    )
    running_a = start_peer_server(root_a)
    running_b = start_peer_server(root_b)
    assert running_a is not None
    assert running_b is not None
    server_a, thread_a = running_a
    server_b, thread_b = running_b
    try:
        client_a = KnowledgePeerClient(root_a)
        client_b = KnowledgePeerClient(root_b)
        assert client_a.health(local_b, PROJECT_ID)["status"] == "READY"
        assert client_b.health(local_a, PROJECT_ID)["status"] == "READY"

        result_b = client_a.query(
            local_b, PROJECT_ID, "RUNTIME_MARKER_B", 5
        )
        result_a = client_b.query(
            local_a, PROJECT_ID, "RUNTIME_MARKER_A", 5
        )
        assert result_b["results"][0]["library_node"] == NODE_B
        assert result_a["results"][0]["library_node"] == NODE_A
        fetched_b = client_a.material(
            local_b, PROJECT_ID, material_b, NODE_B
        )
        fetched_a = client_b.material(
            local_a, PROJECT_ID, material_a, NODE_A
        )
        assert fetched_b["content"] == "RUNTIME_MARKER_B"
        assert fetched_a["content"] == "RUNTIME_MARKER_A"
        assert fetched_a["material_id"] != fetched_b["material_id"]

        body, headers = _signed_query(local_b, secret)
        first = _post(server_a.server_port, body, headers)
        assert first == (200, None)
        _stop(server_a, thread_a)

        replacement_port = _free_port()
        store_a = PeerConfigStore(root_a)
        settings_a = store_a.load()
        store_a.configure_local(
            enabled=True,
            bind_host="127.0.0.1",
            port=replacement_port,
            allow_insecure_lan=False,
            expected_revision=settings_a.revision,
        )
        restarted = start_peer_server(root_a)
        assert restarted is not None
        server_a, thread_a = restarted
        replay = _post(server_a.server_port, body, headers)
        assert replay == (401, "PEER_REPLAY_REJECTED")
    finally:
        _stop(server_a, thread_a)
        _stop(server_b, thread_b)


def test_private_ipv4_contract_requires_explicit_http_risk() -> None:
    private_host = "192.168.50.10"
    private_url = f"http://{private_host}:8766"
    validate_local(True, private_host, 8766, True)
    assert endpoint(private_url, True) == private_url
    with pytest.raises(
        PeerStoreError,
        match="INSECURE_LAN_CONFIRMATION_REQUIRED",
    ):
        validate_local(True, private_host, 8766, False)
    with pytest.raises(
        PeerContractError,
        match="INSECURE_PEER_ENDPOINT",
    ):
        endpoint(private_url, False)


def _write_root(root: Path, node_id: str, marker: str) -> str:
    write_project(root, f"PROJECT_{marker}", PROJECT_ID)
    relative = f"verified/{node_id}.md"
    source = root / relative
    source.parent.mkdir(parents=True, exist_ok=True)
    source.write_text(marker, encoding="utf-8")
    source_hash = hashlib.sha256(marker.encode("utf-8")).hexdigest()
    chunk_id = hashlib.sha256(node_id.encode("utf-8")).hexdigest()
    directory = root / "libraries" / node_id
    build_lexical(directory / "lexical.sqlite3", [
        Chunk(
            chunk_id,
            relative,
            1,
            1,
            marker,
            source_hash,
            "2026-08-26T00:00:00+00:00",
            "TEST",
        )
    ])
    atomic_json(directory / "manifest.json", {
        "schema_version": SCHEMA_VERSION,
        "generation": f"generation-{node_id}",
        "indexed_at": "2026-08-26T00:00:00+00:00",
    })
    store = KnowledgeTreeStore(root / "tree.json")
    current = store.load()
    changed = KnowledgeTree(
        (*current.nodes, LibraryNode(node_id, node_id, "group")),
        (*current.edges, (PROJECT_ID, node_id)),
        revision=current.revision + 1,
    )
    store.save(changed, expected_revision=current.revision)
    return f"library:{chunk_id}"


def _pair(
    root_a: Path,
    root_b: Path,
    port_a: int,
    port_b: int,
) -> tuple[str, str, str]:
    store_a = PeerConfigStore(root_a)
    store_b = PeerConfigStore(root_b)
    settings_a = store_a.load(create=True)
    settings_b = store_b.load(create=True)
    configured_a = store_a.configure_local(
        enabled=True,
        bind_host="127.0.0.1",
        port=port_a,
        allow_insecure_lan=False,
        expected_revision=settings_a.revision,
    )
    configured_b = store_b.configure_local(
        enabled=True,
        bind_host="127.0.0.1",
        port=port_b,
        allow_insecure_lan=False,
        expected_revision=settings_b.revision,
    )
    secret = new_secret()
    store_a.upsert(
        PeerRecord(
            settings_b.local_peer_id,
            "Runtime B",
            PROJECT_ID,
            f"http://127.0.0.1:{port_b}",
            secret,
            NODE_B,
            export_node_id=NODE_A,
        ),
        expected_revision=configured_a.revision,
    )
    store_b.upsert(
        PeerRecord(
            settings_a.local_peer_id,
            "Runtime A",
            PROJECT_ID,
            f"http://127.0.0.1:{port_a}",
            secret,
            NODE_A,
            export_node_id=NODE_B,
        ),
        expected_revision=configured_b.revision,
    )
    return settings_a.local_peer_id, settings_b.local_peer_id, secret


def _signed_query(
    peer_id: str,
    secret: str,
) -> tuple[bytes, dict[str, str]]:
    body = json.dumps(
        {
            "project_id": PROJECT_ID,
            "node_id": NODE_A,
            "query": "RUNTIME_MARKER_A",
            "limit": 5,
        },
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    headers = signed_headers(
        peer_id,
        secret,
        "POST",
        "/v1/query",
        body,
        nonce="R" * 22,
    )
    headers["Content-Type"] = "application/json"
    return body, headers


def _post(
    port: int,
    body: bytes,
    headers: dict[str, str],
) -> tuple[int, str | None]:
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
    try:
        connection.request("POST", "/v1/query", body, headers)
        response = connection.getresponse()
        payload = json.loads(response.read().decode("utf-8"))
        return response.status, payload.get("error")
    finally:
        connection.close()


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def _stop(server: object, thread: object) -> None:
    server.shutdown()
    server.server_close()
    thread.join(timeout=5)
