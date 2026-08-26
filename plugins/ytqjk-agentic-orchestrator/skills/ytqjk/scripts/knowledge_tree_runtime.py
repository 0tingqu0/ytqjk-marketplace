"""Resolve strict leaf-to-root knowledge query chains."""

from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator

from knowledge_tree import KnowledgeTree, LibraryNode
from knowledge_tree_node_query import QueryNode, query_node
from knowledge_tree_store import KnowledgeTreeStore
from rag_common import load_json
from rag_locks import global_lock, project_id_lock


TREE_FILE = "tree.json"
ADD_PROJECT = "ADD_PROJECT"
EXISTING_TREE = "EXISTING_TREE"
INITIALIZE_TREE = "INITIALIZE_TREE"
@dataclass(frozen=True)
class QueryTree:
    revision: int
    chain: tuple[QueryNode, ...]


def capture_bootstrap_intent(
    knowledge_root: Path,
    project_id: str,
) -> str:
    store = KnowledgeTreeStore(knowledge_root / TREE_FILE)
    if not store.path.exists():
        return INITIALIZE_TREE
    tree = store.load()
    nodes = {node.node_id: node for node in tree.nodes}
    catalog = load_json(knowledge_root / "catalog.json", {})
    projects = catalog.get("projects") if type(catalog) is dict else None
    if projects is None:
        projects = {}
    if type(projects) is not dict or any(
        type(node_id) is not str for node_id in projects
    ):
        raise RuntimeError("INVALID_PROJECT_CATALOG")
    missing = set(projects) - set(nodes)
    if missing:
        if project_id in missing:
            raise RuntimeError("CURRENT_PROJECT_TREE_NODE_MISSING")
        raise RuntimeError("PERSISTED_TREE_CATALOG_NODE_MISSING")
    current = nodes.get(project_id)
    if current is not None:
        if current.kind != "project":
            raise RuntimeError("CURRENT_PROJECT_TREE_NODE_INVALID")
        return EXISTING_TREE
    if (knowledge_root / "projects" / project_id).exists():
        raise RuntimeError("CURRENT_PROJECT_TREE_NODE_MISSING")
    return ADD_PROJECT


def bootstrap_query_tree(
    knowledge_root: Path,
    project_id: str,
    intent: str,
) -> QueryTree:
    store = KnowledgeTreeStore(knowledge_root / TREE_FILE)
    if intent in {INITIALIZE_TREE, ADD_PROJECT}:
        tree = store.bootstrap(knowledge_root / "catalog.json")
    elif intent == EXISTING_TREE:
        tree = store.load()
    else:
        raise RuntimeError("INVALID_TREE_BOOTSTRAP_INTENT")
    return _to_query_tree(knowledge_root, project_id, tree)


@contextmanager
def query_tree_transaction(
    knowledge_root: Path,
    expected: QueryTree,
) -> Iterator[QueryTree]:
    store = KnowledgeTreeStore(knowledge_root / TREE_FILE)
    with store.read_transaction() as tree:
        try:
            current = _to_query_tree(
                knowledge_root, expected.chain[0].node_id, tree
            )
        except Exception as error:
            raise RuntimeError("TREE_REVISION_CHANGED") from error
        if current != expected:
            raise RuntimeError("TREE_REVISION_CHANGED")
        yield current


def _to_query_tree(
    knowledge_root: Path,
    project_id: str,
    tree: KnowledgeTree,
) -> QueryTree:
    nodes = {node.node_id: node for node in tree.nodes}
    current = nodes.get(project_id)
    if current is None or current.kind != "project":
        raise RuntimeError("CURRENT_PROJECT_TREE_NODE_MISSING")
    chain = tuple(
        _resolve_node(knowledge_root, nodes[node_id])
        for node_id in tree.ancestors(project_id)
    )
    if not chain or chain[0].node_id != project_id:
        raise RuntimeError("INVALID_CURRENT_PROJECT_QUERY_CHAIN")
    return QueryTree(tree.revision, chain)


def unavailable_node(node: QueryNode, reason: str) -> dict[str, str]:
    return {
        "kind": node.kind,
        "node_id": node.node_id,
        "reason": reason,
        "status": "NOT_CONFIGURED",
    }


def add_tree_evidence(
    result: dict[str, object],
    tree: QueryTree,
    visited: list[str],
    unavailable: list[dict[str, str]],
    hit_node: str | None,
) -> dict[str, object]:
    result.update({
        "hit_node": hit_node,
        "query_chain": [node.node_id for node in tree.chain],
        "tree_revision": tree.revision,
        "unavailable_nodes": unavailable,
        "visited": list(visited),
    })
    return result


def _resolve_node(knowledge_root: Path, node: LibraryNode) -> QueryNode:
    if node.kind == "mounted":
        if node.capability != "query-v1" or node.mount_id is None:
            return QueryNode(
                node.node_id,
                node.kind,
                None,
                None,
                "mounted-fallback",
                "MOUNT_CAPABILITY_UNSUPPORTED",
            )
        return QueryNode(
            node.node_id,
            node.kind,
            None,
            None,
            "peer-same-project-mounted",
            None,
            node.mount_id,
        )
    if node.kind == "project":
        parent = knowledge_root / "projects"
        lock = project_id_lock(knowledge_root, node.node_id)
        scope = "project-source-cache"
    elif node.kind == "global":
        parent = knowledge_root
        lock = global_lock(knowledge_root)
        scope = "global-fallback"
    else:
        parent = knowledge_root / "libraries"
        lock = project_id_lock(knowledge_root, f"library-{node.node_id}")
        scope = "tree-group-fallback"
    name = "global-cache" if node.kind == "global" else node.node_id
    return QueryNode(node.node_id, node.kind, parent / name, lock, scope)
