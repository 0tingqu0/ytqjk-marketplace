from __future__ import annotations

from dataclasses import replace
from pathlib import Path
import sys

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_tree import (  # noqa: E402
    ANCHOR_IMPACT_PENDING,
    KnowledgeTree,
    LibraryNode,
    MAX_REVISION,
    PreviewMismatch,
    RevisionConflict,
    TreeError,
)


def _node(node_id: str, kind: str = "group") -> LibraryNode:
    return LibraryNode(node_id, f"Node {node_id}", kind)


def _tree() -> KnowledgeTree:
    nodes = (
        _node("global", "global"),
        _node("alpha", "project"),
        _node("leaf"),
        _node("sibling", "project"),
        _node("other"),
        _node("bridge"),
        _node("orphan"),
    )
    edges = (
        ("global", "alpha"),
        ("alpha", "leaf"),
        ("global", "sibling"),
    )
    return KnowledgeTree(nodes, edges)


def test_ancestors_are_current_to_root_without_siblings() -> None:
    tree = _tree()

    assert tree.ancestors("leaf") == ("leaf", "alpha", "global")
    assert "sibling" not in tree.ancestors("leaf")
    assert tree.ancestors("other") == ("other",)


def test_attach_requires_matching_preview_and_revision() -> None:
    tree = _tree()
    preview = tree.preview_attach("orphan", "alpha")

    assert preview.old_chain == ("orphan",)
    assert preview.new_chain == ("orphan", "alpha", "global")
    assert preview.subtree_size == 1
    assert preview.anchor_impact == ANCHOR_IMPACT_PENDING
    with pytest.raises(RevisionConflict, match="REVISION_CONFLICT"):
        tree.attach(
            "orphan", "alpha", preview=preview, expected_revision=1,
        )
    forged = replace(preview, new_chain=("orphan", "global"))
    with pytest.raises(PreviewMismatch, match="PREVIEW_MISMATCH"):
        tree.attach(
            "orphan", "alpha", preview=forged, expected_revision=0,
        )

    tree.attach("orphan", "alpha", preview=preview, expected_revision=0)

    assert tree.revision == 1
    assert tree.ancestors("orphan") == preview.new_chain
    with pytest.raises(RevisionConflict, match="REVISION_CONFLICT"):
        tree.attach(
            "orphan", "alpha", preview=preview, expected_revision=0,
        )


def test_preview_cannot_replay_across_same_revision_tree_structure() -> None:
    source = _tree()
    preview = source.preview_attach("orphan", "alpha")
    edges = tuple(
        ("bridge", "sibling") if child == "sibling" else (parent, child)
        for parent, child in source.edges
    )
    target = KnowledgeTree(source.nodes, edges)

    assert target.preview_attach("orphan", "alpha").preview_digest != (
        preview.preview_digest
    )
    with pytest.raises(PreviewMismatch, match="PREVIEW_MISMATCH"):
        target.attach(
            "orphan", "alpha", preview=preview, expected_revision=0,
        )
    assert target.ancestors("orphan") == ("orphan",)


def test_preview_binds_equal_size_subtree_members() -> None:
    source = _tree()
    source_preview = source.preview_move("alpha", "other")
    edges = (
        ("global", "alpha"),
        ("alpha", "sibling"),
        ("global", "leaf"),
    )
    target = KnowledgeTree(source.nodes, edges)
    target_preview = target.preview_move("alpha", "other")

    assert source_preview.subtree_size == target_preview.subtree_size == 2
    assert source_preview.old_chain == target_preview.old_chain
    assert source_preview.new_chain == target_preview.new_chain
    assert source_preview.preview_digest != target_preview.preview_digest
    with pytest.raises(PreviewMismatch, match="PREVIEW_MISMATCH"):
        target.move(
            "alpha", "other", preview=source_preview,
            expected_revision=0,
        )


def test_detach_preserves_descendant_subtree() -> None:
    tree = _tree()
    preview = tree.preview_detach("alpha")

    assert preview.subtree_size == 2
    assert preview.old_chain == ("alpha", "global")
    assert preview.new_chain == ("alpha",)

    tree.detach("alpha", preview=preview, expected_revision=0)

    assert tree.ancestors("alpha") == ("alpha",)
    assert tree.ancestors("leaf") == ("leaf", "alpha")
    assert ("alpha", "leaf") in tree.edges


def test_move_preserves_subtree_and_uses_cas() -> None:
    tree = _tree()
    preview = tree.preview_move("alpha", "other")

    assert preview.subtree_size == 2
    assert preview.old_chain == ("alpha", "global")
    assert preview.new_chain == ("alpha", "other")

    tree.move("alpha", "other", preview=preview, expected_revision=0)

    assert tree.ancestors("leaf") == ("leaf", "alpha", "other")
    assert tree.revision == 1


def test_insert_between_reparents_only_direct_edge() -> None:
    tree = _tree()
    preview = tree.preview_insert_between("global", "alpha", "bridge")

    assert preview.old_chain == ("alpha", "global")
    assert preview.new_chain == ("alpha", "bridge", "global")
    assert preview.subtree_size == 2

    tree.insert_between(
        "global",
        "alpha",
        "bridge",
        preview=preview,
        expected_revision=0,
    )

    assert ("global", "bridge") in tree.edges
    assert ("bridge", "alpha") in tree.edges
    assert ("global", "alpha") not in tree.edges


def test_global_is_an_ordinary_reattachable_node() -> None:
    tree = _tree()
    preview = tree.preview_attach("global", "other")

    tree.attach("global", "other", preview=preview, expected_revision=0)

    assert tree.ancestors("global") == ("global", "other")
    assert tree.ancestors("leaf")[-2:] == ("global", "other")


@pytest.mark.parametrize(
    "operation",
    (
        lambda tree: tree.preview_move("alpha", "leaf"),
        lambda tree: tree.preview_attach("global", "leaf"),
        lambda tree: tree.preview_attach("other", "other"),
        lambda tree: tree.preview_attach("missing", "global"),
        lambda tree: tree.preview_insert_between(
            "global", "alpha", "leaf",
        ),
    ),
)
def test_mutations_reject_cycles_self_unknown_and_multiple_parent(
    operation,
) -> None:
    with pytest.raises(TreeError):
        operation(_tree())


def test_duplicate_and_second_parent_are_distinct_failures() -> None:
    tree = _tree()
    with pytest.raises(TreeError, match="DUPLICATE_EDGE"):
        tree.preview_attach("alpha", "global")
    with pytest.raises(TreeError, match="MULTIPLE_PARENTS"):
        tree.preview_attach("alpha", "other")


@pytest.mark.parametrize(
    "edges, message",
    (
        (
            (("global", "alpha"), ("global", "alpha")),
            "DUPLICATE_EDGE",
        ),
        (
            (("global", "alpha"), ("other", "alpha")),
            "MULTIPLE_PARENTS",
        ),
        (
            (("global", "alpha"), ("alpha", "global")),
            "CYCLE_DETECTED",
        ),
    ),
)
def test_constructor_rejects_duplicate_multiple_parent_and_cycle(
    edges: tuple[tuple[str, str], ...],
    message: str,
) -> None:
    with pytest.raises(TreeError, match=message):
        KnowledgeTree((_node("global"), _node("alpha"), _node("other")),
                      edges)


def test_exact_node_type_and_unique_ids_are_enforced() -> None:
    class ForgedNode(LibraryNode):
        pass

    forged = ForgedNode("forged", "Forged", "group")
    with pytest.raises(TreeError, match="INVALID_NODE_TYPE"):
        KnowledgeTree((forged,))
    with pytest.raises(TreeError, match="DUPLICATE_NODE"):
        KnowledgeTree((_node("same"), _node("same")))
    with pytest.raises(TreeError, match="INVALID_NODE_ID"):
        LibraryNode(" padded ", "Title", "group")


def test_constructor_revalidates_and_copies_nodes() -> None:
    original = _node("original")
    tree = KnowledgeTree((original,))

    object.__setattr__(original, "title", "tampered")

    assert tree.nodes[0].title == "Node original"
    unsafe = _node("unsafe")
    object.__setattr__(unsafe, "node_id", "../escape")
    with pytest.raises(TreeError, match="INVALID_NODE_ID"):
        KnowledgeTree((unsafe,))


def test_public_nodes_are_revalidated_copies() -> None:
    tree = _tree()
    original_edges = tree.edges
    original_preview = tree.preview_move("alpha", "other")
    exposed = next(node for node in tree.nodes if node.node_id == "alpha")

    object.__setattr__(exposed, "node_id", "poisoned")
    object.__setattr__(exposed, "title", "../private")

    assert tree.edges == original_edges
    assert tree.preview_move("alpha", "other") == original_preview
    assert tree.ancestors("leaf") == ("leaf", "alpha", "global")
    assert next(
        node for node in tree.nodes if node.node_id == "alpha"
    ).title == "Node alpha"


def test_revision_is_bounded_and_cannot_overflow() -> None:
    with pytest.raises(TreeError, match="INVALID_REVISION"):
        KnowledgeTree((_node("root"),), revision=MAX_REVISION + 1)
    tree = KnowledgeTree(
        (_node("root"), _node("child")), revision=MAX_REVISION,
    )
    preview = tree.preview_attach("child", "root")

    with pytest.raises(RevisionConflict, match="REVISION_EXHAUSTED"):
        tree.attach(
            "child", "root", preview=preview,
            expected_revision=MAX_REVISION,
        )

    assert tree.revision == MAX_REVISION
    assert tree.edges == ()


def test_mounted_node_contains_only_opaque_reference_metadata() -> None:
    mounted = LibraryNode(
        "mounted-1", "External library", "mounted",
        "mount-1", "read-only",
    )
    assert mounted.mount_id == "mount-1"
    assert mounted.capability == "read-only"
    words = LibraryNode(
        "mounted-2", "Secretariat tokenizer", "mounted",
        "mount-2", "TOKENIZER",
    )
    assert words.title == "Secretariat tokenizer"


@pytest.mark.parametrize(
    "mount_id, capability, title",
    (
        (r"C:\secret\data", "READ_ONLY", "External"),
        (r"\\server\share", "READ_ONLY", "External"),
        ("..", "READ_ONLY", "External"),
        ("ghp_" + "a" * 40, "READ_ONLY", "External"),
        ("mount-1", "https://host", "External"),
        ("mount-1", "READ_ONLY", "mailto:user@example.com"),
        ("mount-1", "READ_ONLY", "../private"),
        ("mount-1", "READ_ONLY", "/home/private"),
        ("mount-1", "READ_ONLY", "api_key=" + "a" * 20),
        ("mount-1", "READ_ONLY", "https://private.example"),
        ("mount-1", "READ_ONLY", "user:password@private-host"),
    ),
)
def test_mounted_node_rejects_paths_network_and_credentials(
    mount_id: str, capability: str, title: str,
) -> None:
    with pytest.raises(TreeError):
        LibraryNode(
            "mounted-1", title, "mounted", mount_id, capability,
        )
