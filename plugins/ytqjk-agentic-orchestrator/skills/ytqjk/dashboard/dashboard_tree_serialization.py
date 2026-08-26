"""Pure JSON serialization for dashboard knowledge-tree responses."""

from __future__ import annotations

import hashlib
import json

from knowledge_tree import KnowledgeTree, LibraryNode, TreePreview


def canonical_digest(value: object) -> str:
    content = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(content).hexdigest()


def tree_payload(tree: KnowledgeTree) -> dict[str, object]:
    parents = {child: parent for parent, child in tree.edges}
    body: dict[str, object] = {
        "revision": tree.revision,
        "nodes": [
            _node_payload(node, parents.get(node.node_id))
            for node in tree.nodes
        ],
        "edges": [
            {"parent_id": parent, "child_id": child}
            for parent, child in tree.edges
        ],
        "roots": sorted(
            node.node_id
            for node in tree.nodes
            if node.node_id not in parents
        ),
    }
    return {**body, "digest": canonical_digest(body)}


def preview_summary(preview: TreePreview) -> dict[str, object]:
    return {
        "node_id": preview.node_id,
        "related_id": preview.related_id,
        "old_parent_id": preview.old_parent_id,
        "new_parent_id": preview.new_parent_id,
        "old_chain": list(preview.old_chain),
        "new_chain": list(preview.new_chain),
        "subtree_size": preview.subtree_size,
        "anchor_impact": preview.anchor_impact,
    }


def _node_payload(
    node: LibraryNode,
    parent_id: str | None,
) -> dict[str, object]:
    metadata: dict[str, str] = {}
    if node.kind == "mounted":
        if node.mount_id is None or node.capability is None:
            raise ValueError("INVALID_MOUNTED_NODE")
        metadata = {
            "mount_id": node.mount_id,
            "capability": node.capability,
        }
    return {
        "id": node.node_id,
        "title": node.title,
        "type": node.kind,
        "parent_id": parent_id,
        "metadata": metadata,
    }


__all__ = ["canonical_digest", "preview_summary", "tree_payload"]
