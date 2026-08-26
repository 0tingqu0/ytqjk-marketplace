"""Resolve the one-way local subtree exported to an authenticated peer."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from knowledge_tree import KnowledgeTree, LibraryNode
from knowledge_tree_store import KnowledgeTreeStore
from project_tracking import require_tracked_project


class PeerScopeError(RuntimeError):
    """A requested peer scope is outside its configured project boundary."""


@dataclass(frozen=True, slots=True)
class PeerLibrary:
    node_id: str
    kind: str
    directory: Path


@dataclass(frozen=True, slots=True)
class PeerExport:
    node_id: str
    title: str
    kind: str

    def public(self) -> dict[str, str]:
        return {
            "id": self.node_id,
            "title": self.title,
            "type": self.kind,
        }


def export_catalog(
    root: Path,
    project_id: str,
    export_node_ids: tuple[str, ...],
) -> tuple[tuple[PeerExport, ...], int]:
    tree = _load_tree(root)
    nodes = {node.node_id: node for node in tree.nodes}
    exports: list[PeerExport] = []
    library_ids: set[str] = set()
    for node_id in export_node_ids:
        libraries = exported_libraries(root, project_id, node_id)
        node = nodes.get(node_id)
        if node is None:
            raise PeerScopeError("PEER_EXPORT_NODE_MISSING")
        exports.append(PeerExport(node.node_id, node.title, node.kind))
        library_ids.update(item.node_id for item in libraries)
    return tuple(exports), len(library_ids)


def exported_libraries(
    root: Path,
    project_id: str,
    export_node_id: str,
) -> tuple[PeerLibrary, ...]:
    _require_project(root, project_id)
    tree = _load_tree(root)
    nodes = {node.node_id: node for node in tree.nodes}
    project = nodes.get(project_id)
    export = nodes.get(export_node_id)
    if project is None or project.kind != "project":
        raise PeerScopeError("PEER_PROJECT_TREE_NODE_MISSING")
    if export is None:
        raise PeerScopeError("PEER_EXPORT_NODE_MISSING")
    project_scope = set(_descendants(tree, project_id))
    if export_node_id not in project_scope:
        raise PeerScopeError("PEER_EXPORT_OUTSIDE_PROJECT")
    if export.kind in {"mounted", "project"} and export_node_id != project_id:
        raise PeerScopeError("PEER_EXPORT_PROJECT_FORBIDDEN")
    node_ids = _exported_descendants(
        tree, nodes, export_node_id, project_id
    )
    return tuple(_library(root, nodes[node_id]) for node_id in node_ids)


def require_exported_library(
    root: Path,
    project_id: str,
    export_node_id: str,
    library_node: str,
) -> PeerLibrary:
    libraries = exported_libraries(root, project_id, export_node_id)
    match = next(
        (item for item in libraries if item.node_id == library_node),
        None,
    )
    if match is None:
        raise PeerScopeError("PEER_LIBRARY_OUTSIDE_EXPORT")
    return match


def _load_tree(root: Path) -> KnowledgeTree:
    try:
        return KnowledgeTreeStore(root / "tree.json").load()
    except (OSError, RuntimeError, ValueError) as error:
        raise PeerScopeError("PEER_TREE_NOT_CONFIGURED") from error


def _require_project(root: Path, project_id: str) -> None:
    try:
        require_tracked_project(root, project_id)
    except (OSError, RuntimeError, ValueError) as error:
        raise PeerScopeError("PEER_PROJECT_NOT_TRACKED") from error


def _descendants(
    tree: KnowledgeTree,
    root_node: str,
) -> tuple[str, ...]:
    children: dict[str, list[str]] = {}
    for parent, child in tree.edges:
        children.setdefault(parent, []).append(child)
    pending = [root_node]
    ordered: list[str] = []
    while pending:
        current = pending.pop(0)
        ordered.append(current)
        pending.extend(sorted(children.get(current, ())))
    return tuple(ordered)


def _exported_descendants(
    tree: KnowledgeTree,
    nodes: dict[str, LibraryNode],
    root_node: str,
    project_id: str,
) -> tuple[str, ...]:
    children: dict[str, list[str]] = {}
    for parent, child in tree.edges:
        children.setdefault(parent, []).append(child)
    pending = [root_node]
    ordered: list[str] = []
    while pending:
        current = pending.pop(0)
        node = nodes[current]
        blocked = node.kind == "mounted"
        blocked = blocked or (
            node.kind == "project" and current != project_id
        )
        if blocked:
            continue
        ordered.append(current)
        pending.extend(sorted(children.get(current, ())))
    return tuple(ordered)


def _library(root: Path, node: LibraryNode) -> PeerLibrary:
    if node.kind == "project":
        directory = root / "projects" / node.node_id
    elif node.kind == "global":
        directory = root / "global-cache"
    else:
        directory = root / "libraries" / node.node_id
    return PeerLibrary(node.node_id, node.kind, directory)


__all__ = [
    "PeerExport",
    "PeerLibrary",
    "PeerScopeError",
    "export_catalog",
    "exported_libraries",
    "require_exported_library",
]
