from __future__ import annotations

import hashlib
import http.client
import json
import sys
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_peer_client import (  # noqa: E402
    KnowledgePeerClient,
    PeerClientError,
    _query_row,
)
from knowledge_peer_contract import signed_headers  # noqa: E402
from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402
from peer_test_support import (  # noqa: E402
    PROJECT_ID,
    pair,
    start_server,
    write_project,
)
from rag_common import (  # noqa: E402
    Chunk,
    SCHEMA_VERSION,
    atomic_json,
    build_lexical,
)


EXPORT_NODE = "authorized-library"
NESTED_NODE = "authorized-child"
SIBLING_NODE = "unrelated-sibling"
FOREIGN_PROJECT = "foreign-project--abcdef012345"
FOREIGN_CHILD = "foreign-project-child"
TRANSITIVE_CHILD = "transitive-child"


def test_peer_reads_only_exported_subtree(tmp_path: Path) -> None:
    client_root = tmp_path / "client"
    server_root = tmp_path / "server"
    write_project(server_root, "PARENT_ONLY_MARKER")
    nested_id = _write_library(
        server_root, NESTED_NODE, "AUTHORIZED_CHILD_MARKER"
    )
    sibling_id = _write_library(
        server_root, SIBLING_NODE, "UNRELATED_SIBLING_MARKER"
    )
    _write_library(
        server_root, TRANSITIVE_CHILD, "TRANSITIVE_MOUNT_MARKER"
    )
    _write_project_index(
        server_root, FOREIGN_PROJECT, "FOREIGN_PROJECT_MARKER"
    )
    _write_library(
        server_root, FOREIGN_CHILD, "FOREIGN_DESCENDANT_MARKER"
    )
    _write_tree(server_root)
    server, thread = start_server(server_root)
    try:
        endpoint = f"http://127.0.0.1:{server.server_port}"
        _, server_id, _ = pair(
            client_root,
            server_root,
            endpoint,
            remote_node_id=EXPORT_NODE,
            server_export_node_id=EXPORT_NODE,
        )
        client = KnowledgePeerClient(client_root)

        hit = client.query(
            server_id, PROJECT_ID, "AUTHORIZED_CHILD_MARKER", 5
        )
        assert hit["status"] == "PEER_HIT"
        assert hit["node_id"] == EXPORT_NODE
        assert hit["results"][0]["library_node"] == NESTED_NODE
        material = client.material(
            server_id,
            PROJECT_ID,
            nested_id,
            NESTED_NODE,
        )
        assert material["content"] == "AUTHORIZED_CHILD_MARKER"

        for marker in (
            "PARENT_ONLY_MARKER",
            "UNRELATED_SIBLING_MARKER",
            "FOREIGN_PROJECT_MARKER",
            "FOREIGN_DESCENDANT_MARKER",
            "TRANSITIVE_MOUNT_MARKER",
        ):
            miss = client.query(server_id, PROJECT_ID, marker, 5)
            assert miss["status"] == "PEER_MISS"
            assert miss["results"] == []

        with pytest.raises(PeerClientError, match="PEER_UNAVAILABLE"):
            client.material(
                server_id,
                PROJECT_ID,
                sibling_id,
                SIBLING_NODE,
            )
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def test_material_and_project_scope_are_enforced_on_server(
    tmp_path: Path,
) -> None:
    client_root = tmp_path / "client"
    server_root = tmp_path / "server"
    write_project(server_root, "PARENT")
    material_id = _write_library(
        server_root, SIBLING_NODE, "OUTSIDE_MATERIAL"
    )
    _write_library(server_root, NESTED_NODE, "INSIDE")
    _write_library(
        server_root, TRANSITIVE_CHILD, "TRANSITIVE_MOUNT_MARKER"
    )
    _write_project_index(server_root, FOREIGN_PROJECT, "FOREIGN")
    _write_library(server_root, FOREIGN_CHILD, "FOREIGN_DESCENDANT")
    _write_tree(server_root)
    server, thread = start_server(server_root)
    try:
        endpoint = f"http://127.0.0.1:{server.server_port}"
        client_id, _, secret = pair(
            client_root,
            server_root,
            endpoint,
            remote_node_id=EXPORT_NODE,
            server_export_node_id=EXPORT_NODE,
        )
        outside = {
            "project_id": PROJECT_ID,
            "node_id": EXPORT_NODE,
            "library_node": SIBLING_NODE,
            "material_id": material_id,
        }
        assert _signed_post(
            server.server_port,
            "/v1/material",
            outside,
            client_id,
            secret,
        ) == (400, "PEER_LIBRARY_OUTSIDE_EXPORT")
        foreign = {
            "project_id": FOREIGN_PROJECT,
            "node_id": EXPORT_NODE,
            "query": "FOREIGN",
            "limit": 5,
        }
        assert _signed_post(
            server.server_port,
            "/v1/query",
            foreign,
            client_id,
            secret,
        ) == (401, "PEER_PROJECT_FORBIDDEN")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def test_client_rejects_oversized_or_extra_result_fields() -> None:
    row = {
        "material_id": "library:" + "a" * 64,
        "library_node": NESTED_NODE,
        "path": "verified/child.md",
        "line_start": 1,
        "line_end": 1,
        "content": "safe",
        "source_sha256": "b" * 64,
        "scope": "peer-group-descendant",
        "score": 1.0,
    }
    _query_row(row)
    with pytest.raises(PeerClientError, match="PEER_RESPONSE_INVALID"):
        _query_row({**row, "unexpected": True})
    with pytest.raises(PeerClientError, match="PEER_RESPONSE_INVALID"):
        _query_row({**row, "content": "x" * 24_001})


def _write_library(root: Path, node_id: str, marker: str) -> str:
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
    return f"library:{chunk_id}"


def _write_project_index(
    root: Path,
    project_id: str,
    marker: str,
) -> None:
    directory = root / "projects" / project_id
    source_hash = hashlib.sha256(marker.encode("utf-8")).hexdigest()
    build_lexical(directory / "lexical.sqlite3", [
        Chunk(
            hashlib.sha256(project_id.encode("utf-8")).hexdigest(),
            "docs/foreign.md",
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
        "source_fingerprint": f"generation-{project_id}",
    })


def _write_tree(root: Path) -> None:
    atomic_json(root / "catalog.json", {
        "schema_version": SCHEMA_VERSION,
        "projects": {
            PROJECT_ID: {"name": "Shared"},
            FOREIGN_PROJECT: {"name": "Foreign"},
        },
    })
    store = KnowledgeTreeStore(root / "tree.json")
    current = store.load()
    nodes = (
        *current.nodes,
        LibraryNode(EXPORT_NODE, "Export", "group"),
        LibraryNode(NESTED_NODE, "Nested", "group"),
        LibraryNode(SIBLING_NODE, "Sibling", "group"),
        LibraryNode(FOREIGN_PROJECT, "Foreign", "project"),
        LibraryNode(FOREIGN_CHILD, "Foreign child", "group"),
        LibraryNode(TRANSITIVE_CHILD, "Transitive child", "group"),
        LibraryNode(
            "transitive-mount",
            "Third peer",
            "mounted",
            "third-peer",
            "query-v1",
        ),
    )
    tree = KnowledgeTree(
        nodes,
        (
            *current.edges,
            (PROJECT_ID, EXPORT_NODE),
            (PROJECT_ID, SIBLING_NODE),
            (EXPORT_NODE, NESTED_NODE),
            (EXPORT_NODE, FOREIGN_PROJECT),
            (FOREIGN_PROJECT, FOREIGN_CHILD),
            (EXPORT_NODE, "transitive-mount"),
            ("transitive-mount", TRANSITIVE_CHILD),
        ),
        revision=current.revision + 1,
    )
    store.save(tree, expected_revision=current.revision)


def _signed_post(
    port: int,
    path: str,
    value: dict[str, object],
    peer_id: str,
    secret: str,
) -> tuple[int, str]:
    body = json.dumps(
        value, separators=(",", ":"), sort_keys=True
    ).encode("utf-8")
    headers = signed_headers(peer_id, secret, "POST", path, body)
    headers["Content-Type"] = "application/json"
    connection = http.client.HTTPConnection(
        "127.0.0.1", port, timeout=5
    )
    try:
        connection.request("POST", path, body=body, headers=headers)
        response = connection.getresponse()
        payload = json.loads(response.read().decode("utf-8"))
        return response.status, str(payload.get("error", ""))
    finally:
        connection.close()
