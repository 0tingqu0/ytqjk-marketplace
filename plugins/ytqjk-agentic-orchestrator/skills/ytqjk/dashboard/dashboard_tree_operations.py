"""Validation and mutation helpers for dashboard tree API."""

from __future__ import annotations

import re

from dashboard_tree_serialization import preview_summary
from knowledge_tree import (
    ANCHOR_IMPACT_PENDING,
    MAX_REVISION,
    KnowledgeTree,
    LibraryNode,
    RevisionConflict,
    TreeError,
    TreePreview,
)


ACTIONS = {
    "attach": frozenset({"node_id", "parent_id"}),
    "create": frozenset({
        "node_id", "title", "type", "parent_id", "metadata",
    }),
    "detach": frozenset({"node_id"}),
    "insert_between": frozenset({
        "parent_id", "node_id", "middle_id",
    }),
    "move": frozenset({"node_id", "parent_id"}),
    "rebuild_index": frozenset({"node_id", "document_ids"}),
}
_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
_DIGEST = re.compile(r"^[0-9a-f]{64}$")


class DashboardTreeApiError(ValueError):
    def __init__(self, status: int, code: str) -> None:
        super().__init__(code)
        self.status = status
        self.code = code

    def payload(self) -> dict[str, object]:
        return {
            "ok": False,
            "error": {
                "status": self.status,
                "code": self.code,
                "message": self.code,
            },
        }


def validate_action(value: object) -> str:
    if type(value) is not str or value not in ACTIONS:
        raise DashboardTreeApiError(400, "INVALID_TREE_ACTION")
    return value


def validate_arguments(
    action: str,
    payload: object,
) -> dict[str, object]:
    value = exact_object(payload, ACTIONS[action])
    if action == "rebuild_index":
        _identifier("node_id", value["node_id"])
        documents = value["document_ids"]
        if type(documents) is not list or any(
            type(item) is not str
            or _DIGEST.fullmatch(item) is None
            for item in documents
        ):
            raise DashboardTreeApiError(
                400, "INVALID_DOCUMENT_IDS"
            )
        if len(documents) != len(set(documents)):
            raise DashboardTreeApiError(
                400, "DUPLICATE_DOCUMENT_ID"
            )
        return {
            "node_id": value["node_id"],
            "document_ids": list(documents),
        }
    for name in ACTIONS[action] - {"title", "type", "metadata"}:
        if name == "parent_id" and action == "create":
            if value[name] is None:
                continue
        _identifier(name, value[name])
    if action != "create":
        return dict(value)
    kind = value["type"]
    metadata = value["metadata"]
    if type(kind) is not str or kind not in {"group", "mounted"}:
        raise DashboardTreeApiError(400, "CREATION_TYPE_FORBIDDEN")
    if type(metadata) is not dict:
        raise DashboardTreeApiError(400, "INVALID_NODE_METADATA")
    if kind == "group" and metadata:
        raise DashboardTreeApiError(400, "GROUP_METADATA_FORBIDDEN")
    required = frozenset({"mount_id", "capability"})
    if kind == "mounted" and set(metadata) != required:
        raise DashboardTreeApiError(400, "INVALID_MOUNT_METADATA")
    try:
        new_node(value)
    except TreeError as error:
        raise tree_error(error) from error
    copied = dict(value)
    copied["metadata"] = dict(metadata)
    return copied


def validate_commit(payload: object) -> tuple[str, int]:
    value = exact_object(
        payload, frozenset({"digest", "expected_revision"}),
    )
    digest = value["digest"]
    revision = value["expected_revision"]
    if type(digest) is not str or _DIGEST.fullmatch(digest) is None:
        raise DashboardTreeApiError(400, "INVALID_PREVIEW_DIGEST")
    if (
        type(revision) is not int
        or revision < 0
        or revision > MAX_REVISION
    ):
        raise DashboardTreeApiError(400, "INVALID_EXPECTED_REVISION")
    return digest, revision


def plan(
    tree: KnowledgeTree,
    action: str,
    arguments: dict[str, object],
) -> dict[str, object]:
    if action == "rebuild_index":
        node_id = str(arguments["node_id"])
        node = next(
            (item for item in tree.nodes if item.node_id == node_id),
            None,
        )
        if node is None:
            raise TreeError("UNKNOWN_NODE")
        if node.kind != "group":
            raise TreeError("GROUP_NODE_REQUIRED")
        return {
            "node_id": node_id,
            "operation": "REBUILD_GROUP_INDEX",
            "document_count": len(arguments["document_ids"]),
            "source_scope": "approved-verified-only",
        }
    if action == "create":
        create_tree(tree, arguments)
        parent = arguments["parent_id"]
        chain = [arguments["node_id"]]
        if parent is not None:
            chain.extend(tree.ancestors(str(parent)))
        return {
            "node_id": arguments["node_id"],
            "related_id": parent,
            "old_parent_id": None,
            "new_parent_id": parent,
            "old_chain": [],
            "new_chain": chain,
            "subtree_size": 1,
            "anchor_impact": ANCHOR_IMPACT_PENDING,
        }
    return preview_summary(core_preview(tree, action, arguments))


def apply(
    tree: KnowledgeTree,
    action: str,
    arguments: dict[str, object],
) -> KnowledgeTree:
    if action == "rebuild_index":
        raise TreeError("MATERIALIZATION_REQUIRES_SERVICE")
    if action == "create":
        return create_tree(tree, arguments)
    preview = core_preview(tree, action, arguments)
    core_commit(tree, action, arguments, preview)
    return tree


def create_tree(
    tree: KnowledgeTree,
    arguments: dict[str, object],
) -> KnowledgeTree:
    if tree.revision == MAX_REVISION:
        raise RevisionConflict("REVISION_EXHAUSTED")
    node = new_node(arguments)
    if any(item.node_id == node.node_id for item in tree.nodes):
        raise TreeError("DUPLICATE_NODE")
    edges = list(tree.edges)
    parent = arguments["parent_id"]
    if parent is not None:
        tree.ancestors(str(parent))
        edges.append((str(parent), node.node_id))
    return KnowledgeTree(
        (*tree.nodes, node), edges, revision=tree.revision + 1,
    )


def affected_nodes(
    tree: KnowledgeTree,
    action: str,
    arguments: dict[str, object],
) -> list[str]:
    node_id = str(arguments["node_id"])
    found = {node_id}
    if action not in {"create", "rebuild_index"}:
        found.update(_subtree(tree, node_id))
    for name in ("parent_id", "middle_id"):
        value = arguments.get(name)
        if value is not None:
            found.add(str(value))
    return sorted(found)


def exact_object(
    value: object,
    required: frozenset[str],
) -> dict[str, object]:
    if type(value) is not dict or set(value) != required:
        raise DashboardTreeApiError(400, "INVALID_REQUEST_FIELDS")
    return value


def new_node(arguments: dict[str, object]) -> LibraryNode:
    metadata = arguments["metadata"]
    if type(metadata) is not dict:
        raise TreeError("INVALID_NODE_METADATA")
    return LibraryNode(
        str(arguments["node_id"]),
        arguments["title"],
        str(arguments["type"]),
        metadata.get("mount_id"),
        metadata.get("capability"),
    )


def core_preview(
    tree: KnowledgeTree,
    action: str,
    arguments: dict[str, object],
) -> TreePreview:
    node = str(arguments["node_id"])
    if action == "attach":
        return tree.preview_attach(node, str(arguments["parent_id"]))
    if action == "detach":
        return tree.preview_detach(node)
    if action == "move":
        return tree.preview_move(node, str(arguments["parent_id"]))
    return tree.preview_insert_between(
        str(arguments["parent_id"]),
        node,
        str(arguments["middle_id"]),
    )


def core_commit(
    tree: KnowledgeTree,
    action: str,
    arguments: dict[str, object],
    preview: TreePreview,
) -> None:
    node = str(arguments["node_id"])
    revision = tree.revision
    if action == "attach":
        tree.attach(
            node, str(arguments["parent_id"]),
            preview=preview, expected_revision=revision,
        )
    elif action == "detach":
        tree.detach(node, preview=preview, expected_revision=revision)
    elif action == "move":
        tree.move(
            node, str(arguments["parent_id"]),
            preview=preview, expected_revision=revision,
        )
    else:
        tree.insert_between(
            str(arguments["parent_id"]), node,
            str(arguments["middle_id"]),
            preview=preview, expected_revision=revision,
        )


def tree_error(error: Exception) -> DashboardTreeApiError:
    code = str(error)
    if code == "UNKNOWN_NODE":
        return DashboardTreeApiError(404, code)
    conflicts = (
        "CYCLE", "DUPLICATE", "EDGE_", "MULTIPLE", "NODE_",
        "PREVIEW", "REVISION", "SELF_PARENT", "TOPOLOGY",
    )
    status = 409 if code.startswith(conflicts) else 400
    return DashboardTreeApiError(status, code or "TREE_OPERATION_FAILED")


def _identifier(name: str, value: object) -> str:
    if type(value) is not str or _IDENTIFIER.fullmatch(value) is None:
        raise DashboardTreeApiError(400, f"INVALID_{name.upper()}")
    return value


def _subtree(tree: KnowledgeTree, node_id: str) -> set[str]:
    children: dict[str, list[str]] = {}
    for parent, child in tree.edges:
        children.setdefault(parent, []).append(child)
    pending = [node_id]
    found: set[str] = set()
    while pending:
        current = pending.pop()
        if current not in found:
            found.add(current)
            pending.extend(children.get(current, ()))
    return found


__all__ = [
    "DashboardTreeApiError",
    "affected_nodes",
    "apply",
    "plan",
    "tree_error",
    "validate_action",
    "validate_arguments",
    "validate_commit",
]
