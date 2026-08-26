from __future__ import annotations

import sys
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from global_session_query import query_global  # noqa: E402
from knowledge_peer_dispatch import dispatch_siblings  # noqa: E402
from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402
from peer_test_support import pair, start_server, write_project  # noqa: E402
from project_tracking import identify_project, track_project  # noqa: E402


def test_mounted_ancestor_is_real_same_project_fallback(
    tmp_path: Path,
) -> None:
    project = tmp_path / "work"
    project.mkdir()
    client_root = tmp_path / "client-knowledge"
    server_root = tmp_path / "server-knowledge"
    identity = identify_project(project)
    project_id = identity["id"]
    track_project(client_root, project, identity)
    write_project(server_root, "REMOTE_ANCESTOR_MARKER", project_id)
    server, thread = start_server(server_root)
    try:
        endpoint = f"http://127.0.0.1:{server.server_port}"
        _, server_id, _ = pair(
            client_root, server_root, endpoint, project_id
        )
        store = KnowledgeTreeStore(client_root / "tree.json")
        current = store.bootstrap(client_root / "catalog.json")
        mounted = LibraryNode(
            "remote-library",
            "Remote library",
            "mounted",
            server_id,
            "query-v1",
        )
        edges = [
            edge for edge in current.edges
            if edge[1] != project_id
        ]
        edges.extend((
            ("global", mounted.node_id),
            (mounted.node_id, project_id),
        ))
        changed = KnowledgeTree(
            (*current.nodes, mounted),
            edges,
            revision=current.revision + 1,
        )
        store.save(changed, expected_revision=current.revision)

        result = query_global(
            client_root,
            project,
            "REMOTE_ANCESTOR_MARKER",
            "session-peer-ancestor",
            project_id,
            5,
        )

        assert result["status"] == "PEER_FALLBACK_HIT"
        assert result["scope"] == "peer-same-project-mounted"
        assert result["hit_node"] == mounted.node_id
        assert result["result_count"] == 1
        assert result["prefetch_count"] == 0
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def test_explicit_dispatch_visits_only_mounted_siblings(
    tmp_path: Path,
) -> None:
    project = tmp_path / "work"
    project.mkdir()
    client_root = tmp_path / "client-knowledge"
    server_root = tmp_path / "server-knowledge"
    identity = identify_project(project)
    project_id = identity["id"]
    track_project(client_root, project, identity)
    write_project(server_root, "REMOTE_SIBLING_MARKER", project_id)
    server, thread = start_server(server_root)
    try:
        endpoint = f"http://127.0.0.1:{server.server_port}"
        _, server_id, _ = pair(
            client_root, server_root, endpoint, project_id
        )
        store = KnowledgeTreeStore(client_root / "tree.json")
        current = store.bootstrap(client_root / "catalog.json")
        mounted = LibraryNode(
            "remote-sibling",
            "Remote sibling",
            "mounted",
            server_id,
            "query-v1",
        )
        changed = KnowledgeTree(
            (*current.nodes, mounted),
            (*current.edges, ("global", mounted.node_id)),
            revision=current.revision + 1,
        )
        store.save(changed, expected_revision=current.revision)

        result = dispatch_siblings(
            client_root,
            project_id,
            "REMOTE_SIBLING_MARKER",
            5,
        )

        assert result["status"] == "PEER_DISPATCH_HIT"
        assert result["scope"] == "explicit-same-parent-peer-dispatch"
        assert result["result_count"] == 1
        assert result["results"][0]["library_node"] == project_id
        assert result["results"][0]["mount_node"] == mounted.node_id
        assert len(result["peers"]) == 1
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
