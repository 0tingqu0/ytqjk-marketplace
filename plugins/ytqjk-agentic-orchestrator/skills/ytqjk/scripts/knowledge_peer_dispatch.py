"""Explicit sibling-peer dispatch without weakening normal query isolation."""

from __future__ import annotations

from pathlib import Path

from knowledge_peer_client import KnowledgePeerClient, PeerClientError
from knowledge_tree import KnowledgeTree
from knowledge_tree_store import KnowledgeTreeStore
from project_tracking import require_tracked_project


def dispatch_siblings(
    root: Path,
    project_id: str,
    query: str,
    limit: int,
    *,
    client: KnowledgePeerClient | None = None,
) -> dict[str, object]:
    if type(query) is not str or not query.strip() or len(query) > 2000:
        raise ValueError("INVALID_PEER_QUERY")
    if type(limit) is not int or not 1 <= limit <= 20:
        raise ValueError("INVALID_PEER_LIMIT")
    require_tracked_project(root, project_id)
    tree = KnowledgeTreeStore(root / "tree.json").load()
    mounts = sibling_mounts(tree, project_id)
    peer_client = client or KnowledgePeerClient(root)
    results: list[dict[str, object]] = []
    peers: list[dict[str, object]] = []
    remaining = limit
    for node_id, mount_id in mounts:
        if remaining < 1:
            break
        try:
            response = peer_client.query(
                mount_id, project_id, query, remaining
            )
            rows = response.get("results", [])
            if type(rows) is not list:
                raise PeerClientError("PEER_RESPONSE_INVALID")
            for row in rows:
                if type(row) is not dict:
                    raise PeerClientError("PEER_RESPONSE_INVALID")
                results.append({
                    **row,
                    "peer_id": response.get("peer_id"),
                    "mount_node": node_id,
                })
            remaining = limit - len(results)
            peers.append({
                "node_id": node_id,
                "mount_id": mount_id,
                "status": response.get("status", "PEER_MISS"),
                "result_count": len(rows),
            })
        except PeerClientError as error:
            peers.append({
                "node_id": node_id,
                "mount_id": mount_id,
                "status": "UNAVAILABLE",
                "reason": str(error),
                "result_count": 0,
            })
    available = sum(item["status"] != "UNAVAILABLE" for item in peers)
    status = "PEER_DISPATCH_HIT" if results else "PEER_DISPATCH_MISS"
    if available == 0 and peers:
        status = "PEER_DISPATCH_UNAVAILABLE"
    return {
        "ok": True,
        "status": status,
        "project_id": project_id,
        "scope": "explicit-same-parent-peer-dispatch",
        "result_count": len(results),
        "results": results,
        "peers": peers,
    }


def sibling_mounts(
    tree: KnowledgeTree,
    project_id: str,
) -> tuple[tuple[str, str], ...]:
    nodes = {item.node_id: item for item in tree.nodes}
    current = nodes.get(project_id)
    if current is None or current.kind != "project":
        raise ValueError("CURRENT_PROJECT_TREE_NODE_MISSING")
    parents = {child: parent for parent, child in tree.edges}
    parent = parents.get(project_id)
    if parent is None:
        return ()
    result: list[tuple[str, str]] = []
    for node_id, node in nodes.items():
        if (
            node_id != project_id
            and parents.get(node_id) == parent
            and node.kind == "mounted"
            and node.capability == "query-v1"
            and node.mount_id is not None
        ):
            result.append((node_id, node.mount_id))
    return tuple(sorted(result))


def fetch_sibling_material(
    root: Path,
    project_id: str,
    node_id: str,
    material_id: str,
    *,
    remote_library_node: str | None = None,
    client: KnowledgePeerClient | None = None,
) -> dict[str, object]:
    tree = KnowledgeTreeStore(root / "tree.json").load()
    mounts = dict(sibling_mounts(tree, project_id))
    mount_id = mounts.get(node_id)
    if mount_id is None:
        raise ValueError("PEER_LIBRARY_NOT_SIBLING")
    value = (client or KnowledgePeerClient(root)).material(
        mount_id,
        project_id,
        material_id,
        remote_library_node,
    )
    return {
        "ok": True,
        "status": "PEER_MATERIAL_READY",
        "project_id": project_id,
        "mount_node": node_id,
        "library_node": value["library_node"],
        "material": value,
    }


__all__ = [
    "dispatch_siblings",
    "fetch_sibling_material",
    "sibling_mounts",
]
