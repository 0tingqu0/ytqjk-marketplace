from __future__ import annotations

import hashlib
import threading
from http.server import ThreadingHTTPServer
from pathlib import Path

from knowledge_peer_contract import PeerRecord, new_secret
from knowledge_peer_server import KnowledgePeerHandler, ReplayGuard
from knowledge_peer_store import PeerConfigStore
from knowledge_tree_store import KnowledgeTreeStore
from rag_common import Chunk, SCHEMA_VERSION, atomic_json, build_lexical


PROJECT_ID = "shared-project--0123456789ab"


def write_project(
    root: Path,
    marker: str,
    project_id: str = PROJECT_ID,
) -> None:
    project = root / "projects" / project_id
    project.mkdir(parents=True, exist_ok=True)
    atomic_json(root / "catalog.json", {
        "schema_version": SCHEMA_VERSION,
        "projects": {project_id: {"name": "Shared"}},
    })
    KnowledgeTreeStore(root / "tree.json").bootstrap(
        root / "catalog.json"
    )
    source_hash = hashlib.sha256(marker.encode("utf-8")).hexdigest()
    build_lexical(project / "lexical.sqlite3", [
        Chunk(
            hashlib.sha256(b"peer-chunk").hexdigest(),
            "docs/shared.md",
            1,
            1,
            marker,
            source_hash,
            "2026-08-26T00:00:00+00:00",
            "TEST",
        )
    ])
    atomic_json(project / "manifest.json", {
        "schema_version": SCHEMA_VERSION,
        "source_fingerprint": "peer-generation-1",
        "indexed_at": "2026-08-26T00:00:00+00:00",
    })


def start_server(
    root: Path,
) -> tuple[ThreadingHTTPServer, threading.Thread]:
    attributes = {
        "knowledge_root": root.resolve(),
        "store": PeerConfigStore(root),
        "replay": ReplayGuard(),
    }
    handler = type("TestPeerHandler", (KnowledgePeerHandler,), attributes)
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, thread


def pair(
    client_root: Path,
    server_root: Path,
    endpoint: str,
    project_id: str = PROJECT_ID,
    remote_node_id: str | None = None,
    client_export_node_id: str | None = None,
    server_export_node_id: str | None = None,
) -> tuple[str, str, str]:
    secret = new_secret()
    client_store = PeerConfigStore(client_root)
    server_store = PeerConfigStore(server_root)
    client = client_store.load(create=True)
    server = server_store.load(create=True)
    remote_node = remote_node_id or project_id
    client_store.upsert(
        PeerRecord(
            server.local_peer_id,
            "Server",
            project_id,
            endpoint,
            secret,
            remote_node,
            export_node_id=client_export_node_id or project_id,
        ),
        expected_revision=client.revision,
    )
    server_store.upsert(
        PeerRecord(
            client.local_peer_id,
            "Client",
            project_id,
            "http://127.0.0.1:9",
            secret,
            project_id,
            export_node_id=server_export_node_id or remote_node,
        ),
        expected_revision=server.revision,
    )
    return client.local_peer_id, server.local_peer_id, secret
