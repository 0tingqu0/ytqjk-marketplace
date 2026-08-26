from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass, replace
from typing import Callable, Iterable


ANCHOR_IMPACT_PENDING = "NOT_EVALUATED"
MAX_REVISION = 2**63 - 1
_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
_UNSAFE_MOUNT = re.compile(
    r"(?i)(?:(?<![a-z0-9+.-])[a-z][a-z0-9+.-]*:\S|\\\\|"
    r"(?:^|\s)/\S|(?:^|[\\/])\.\.(?:[\\/]|$)|AKIA[A-Z0-9]{16}|"
    r"gh[pousr]_\w{30,}|(?:(?:api.?key|access.?token|client.?secret)|"
    r"password|credential)\s*[:=]\s*\S{8,}|[^\s:]+:[^\s@]+@[^\s]+)"
)


class TreeError(ValueError): ...


class RevisionConflict(TreeError): ...


class PreviewMismatch(TreeError): ...


def _text(name: str, value: object, limit: int = 200) -> str:
    invalid = type(value) is not str or not value.strip() or len(value) > limit
    if invalid or value != value.strip():
        raise TreeError(f"INVALID_{name.upper()}")
    if any(ord(char) < 32 or ord(char) == 127 for char in value):
        raise TreeError(f"INVALID_{name.upper()}")
    return value.strip()


def _identifier(name: str, value: object) -> str:
    text = _text(name, value, 128)
    if _IDENTIFIER.fullmatch(text) is None:
        raise TreeError(f"INVALID_{name.upper()}")
    return text


def _digest(value: object) -> str:
    payload = json.dumps(value, ensure_ascii=False, allow_nan=False,
                         separators=(",", ":"), sort_keys=True)
    return hashlib.sha256(payload.encode("utf-8")).hexdigest()


@dataclass(frozen=True)
class LibraryNode:
    node_id: str
    title: str
    kind: str
    mount_id: str | None = None
    capability: str | None = None

    def __post_init__(self) -> None:
        _identifier("node_id", self.node_id)
        title = _text("title", self.title)
        kind = _text("kind", self.kind, 16)
        if kind not in ("global", "group", "mounted", "project"):
            raise TreeError("INVALID_NODE_KIND")
        if kind == "mounted":
            mount = _identifier("mount_id", self.mount_id)
            capability = _text("capability", self.capability, 64)
            if _IDENTIFIER.fullmatch(capability) is None:
                raise TreeError("INVALID_CAPABILITY")
            values = (title, mount, capability)
            if any(_UNSAFE_MOUNT.search(value) for value in values):
                raise TreeError("UNSAFE_MOUNT_METADATA")
        elif self.mount_id is not None or self.capability is not None:
            raise TreeError("MOUNT_METADATA_FORBIDDEN")


@dataclass(frozen=True)
class TreePreview:
    operation: str
    node_id: str
    related_id: str | None
    old_parent_id: str | None
    new_parent_id: str | None
    old_chain: tuple[str, ...]
    new_chain: tuple[str, ...]
    subtree_size: int
    anchor_impact: str
    base_revision: int
    preview_digest: str


class KnowledgeTree:
    def __init__(
        self,
        nodes: Iterable[LibraryNode],
        edges: Iterable[tuple[str, str]] = (),
        *,
        revision: int = 0,
    ) -> None:
        valid = type(revision) is int and 0 <= revision <= MAX_REVISION
        if not valid:
            raise TreeError("INVALID_REVISION")
        self._revision = revision
        self._nodes: dict[str, LibraryNode] = {}
        for node in nodes:
            if type(node) is not LibraryNode:
                raise TreeError("INVALID_NODE_TYPE")
            node = LibraryNode(node.node_id, node.title, node.kind,
                               node.mount_id, node.capability)
            if node.node_id in self._nodes:
                raise TreeError("DUPLICATE_NODE")
            self._nodes[node.node_id] = node
        self._parents: dict[str, str] = {}
        seen_edges: set[tuple[str, str]] = set()
        for edge in edges:
            if type(edge) is not tuple or len(edge) != 2:
                raise TreeError("INVALID_EDGE")
            parent, child = edge
            self._require_nodes(parent, child)
            if parent == child:
                raise TreeError("SELF_PARENT")
            if edge in seen_edges:
                raise TreeError("DUPLICATE_EDGE")
            if child in self._parents:
                raise TreeError("MULTIPLE_PARENTS")
            seen_edges.add(edge)
            self._parents[child] = parent
        self._validate_acyclic(self._parents)

    @property
    def revision(self) -> int:
        return self._revision

    @property
    def nodes(self) -> tuple[LibraryNode, ...]:
        return tuple(replace(self._nodes[key]) for key in sorted(self._nodes))

    @property
    def edges(self) -> tuple[tuple[str, str], ...]:
        edges = ((parent, child) for child, parent in self._parents.items())
        return tuple(sorted(edges))

    def ancestors(self, node_id: str) -> tuple[str, ...]:
        self._require_nodes(node_id)
        return self._chain(node_id, self._parents)

    def preview_attach(self, node_id: str, parent_id: str) -> TreePreview:
        return self._attach_plan(node_id, parent_id)[0]

    def attach(
        self,
        node_id: str,
        parent_id: str,
        *,
        preview: TreePreview,
        expected_revision: int,
    ) -> None:
        self._commit(
            lambda: self._attach_plan(node_id, parent_id),
            preview, expected_revision,
        )

    def preview_detach(self, node_id: str) -> TreePreview:
        return self._detach_plan(node_id)[0]

    def detach(
        self,
        node_id: str,
        *,
        preview: TreePreview,
        expected_revision: int,
    ) -> None:
        self._commit(
            lambda: self._detach_plan(node_id), preview, expected_revision,
        )

    def preview_move(self, node_id: str, parent_id: str) -> TreePreview:
        return self._move_plan(node_id, parent_id)[0]

    def move(
        self,
        node_id: str,
        parent_id: str,
        *,
        preview: TreePreview,
        expected_revision: int,
    ) -> None:
        self._commit(
            lambda: self._move_plan(node_id, parent_id),
            preview, expected_revision,
        )

    def preview_insert_between(
        self, parent_id: str, node_id: str, middle_id: str,
    ) -> TreePreview:
        return self._insert_plan(parent_id, node_id, middle_id)[0]

    def insert_between(
        self,
        parent_id: str,
        node_id: str,
        middle_id: str,
        *,
        preview: TreePreview,
        expected_revision: int,
    ) -> None:
        self._commit(
            lambda: self._insert_plan(parent_id, node_id, middle_id),
            preview, expected_revision,
        )

    def _attach_plan(
        self, node_id: str, parent_id: str,
    ) -> tuple[TreePreview, dict[str, str]]:
        self._require_nodes(node_id, parent_id)
        if node_id == parent_id:
            raise TreeError("SELF_PARENT")
        if node_id in self._parents:
            code = (
                "DUPLICATE_EDGE"
                if self._parents[node_id] == parent_id
                else "MULTIPLE_PARENTS"
            )
            raise TreeError(code)
        parents = dict(self._parents)
        parents[node_id] = parent_id
        return self._plan("attach", node_id, parent_id, parents), parents

    def _detach_plan(
        self, node_id: str,
    ) -> tuple[TreePreview, dict[str, str]]:
        self._require_nodes(node_id)
        if node_id not in self._parents:
            raise TreeError("NODE_ALREADY_ROOT")
        parents = dict(self._parents)
        old_parent = parents.pop(node_id)
        return self._plan("detach", node_id, old_parent, parents), parents

    def _move_plan(
        self, node_id: str, parent_id: str,
    ) -> tuple[TreePreview, dict[str, str]]:
        self._require_nodes(node_id, parent_id)
        if node_id == parent_id:
            raise TreeError("SELF_PARENT")
        if node_id not in self._parents:
            raise TreeError("NODE_IS_ROOT")
        if self._parents[node_id] == parent_id:
            raise TreeError("DUPLICATE_EDGE")
        parents = dict(self._parents)
        parents[node_id] = parent_id
        return self._plan("move", node_id, parent_id, parents), parents

    def _insert_plan(
        self, parent_id: str, node_id: str, middle_id: str,
    ) -> tuple[TreePreview, dict[str, str]]:
        self._require_nodes(parent_id, node_id, middle_id)
        if len({parent_id, node_id, middle_id}) != 3:
            raise TreeError("SELF_PARENT")
        if self._parents.get(node_id) != parent_id:
            raise TreeError("EDGE_NOT_FOUND")
        if middle_id in self._parents:
            raise TreeError("MULTIPLE_PARENTS")
        parents = dict(self._parents)
        parents[middle_id] = parent_id
        parents[node_id] = middle_id
        preview = self._plan("insert_between", node_id, middle_id, parents)
        return preview, parents

    def _plan(
        self,
        operation: str,
        node_id: str,
        related_id: str,
        new_parents: dict[str, str],
    ) -> TreePreview:
        self._validate_acyclic(new_parents)
        old_parent = self._parents.get(node_id)
        new_parent = new_parents.get(node_id)
        members = self._subtree_members(node_id)
        values = (
            operation, node_id, related_id, old_parent, new_parent,
            self._chain(node_id, self._parents),
            self._chain(node_id, new_parents), len(members),
            ANCHOR_IMPACT_PENDING, self._revision,
        )
        binding = (values, members, tuple(sorted(self._nodes)), self.edges)
        return TreePreview(*values, _digest(binding))

    def _commit(
        self,
        plan: Callable[[], tuple[TreePreview, dict[str, str]]],
        preview: TreePreview,
        expected_revision: int,
    ) -> None:
        if type(expected_revision) is not int:
            raise RevisionConflict("INVALID_EXPECTED_REVISION")
        if expected_revision != self._revision:
            raise RevisionConflict("REVISION_CONFLICT")
        if self._revision == MAX_REVISION:
            raise RevisionConflict("REVISION_EXHAUSTED")
        actual, parents = plan()
        if type(preview) is not TreePreview or preview != actual:
            raise PreviewMismatch("PREVIEW_MISMATCH")
        self._parents = parents
        self._revision += 1

    def _require_nodes(self, *node_ids: str) -> None:
        unknown = any(type(value) is not str or value not in self._nodes
                      for value in node_ids)
        if unknown:
            raise TreeError("UNKNOWN_NODE")

    def _chain(
        self, node_id: str, parents: dict[str, str],
    ) -> tuple[str, ...]:
        chain = [node_id]
        while chain[-1] in parents:
            chain.append(parents[chain[-1]])
        return tuple(chain)

    def _subtree_members(self, node_id: str) -> tuple[str, ...]:
        pending = [node_id]
        found: set[str] = set()
        while pending:
            current = pending.pop()
            if current in found:
                continue
            found.add(current)
            pending.extend(
                child for child, parent in self._parents.items()
                if parent == current
            )
        return tuple(sorted(found))

    def _validate_acyclic(self, parents: dict[str, str]) -> None:
        for node_id in self._nodes:
            seen: set[str] = set()
            current = node_id
            while current in parents:
                if current in seen:
                    raise TreeError("CYCLE_DETECTED")
                seen.add(current)
                current = parents[current]
