from __future__ import annotations

import hashlib
import sys
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from global_session_query import query_global  # noqa: E402
from global_store import chunks_fingerprint, scan_global  # noqa: E402
from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_runtime import TREE_FILE  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402
from knowledge_tree_store import TreeStoreError  # noqa: E402
from project_prefetch import list_prefetch  # noqa: E402
from project_tracking import identify_project, track_project  # noqa: E402
from rag_common import DEFAULT_CONFIG, Chunk, SCHEMA_VERSION  # noqa: E402
from rag_common import atomic_json, build_lexical  # noqa: E402
from rag_common import config_fingerprint, utc_now  # noqa: E402


def _project(base: Path, name: str) -> tuple[Path, str]:
    root = base / name
    root.mkdir()
    return root, identify_project(root)["id"]


def _track(knowledge: Path, projects: list[Path]) -> None:
    for project in projects:
        track_project(knowledge, project)


def _node_index(
    cache: Path,
    marker: str | None,
    *,
    path: str = "verified/fact.md",
) -> None:
    cache.mkdir(parents=True, exist_ok=True)
    chunks = []
    if marker is not None:
        knowledge = cache.parents[1]
        source = knowledge / path
        source.parent.mkdir(parents=True, exist_ok=True)
        source.write_text(marker, encoding="utf-8")
        source_hash = hashlib.sha256(
            marker.encode("utf-8")
        ).hexdigest()
        chunks.append(
            Chunk(
                "chunk-1",
                path,
                1,
                1,
                marker,
                source_hash,
                utc_now(),
                "HEAD",
            )
        )
    build_lexical(cache / "lexical.sqlite3", chunks)
    atomic_json(
        cache / "manifest.json",
        {
            "config_fingerprint": config_fingerprint(DEFAULT_CONFIG),
            "generation": f"generation-{marker or 'empty'}",
            "indexed_at": utc_now(),
            "schema_version": SCHEMA_VERSION,
            "stats": {"chunks": len(chunks), "text_bytes": 0},
            "vector": {"enabled": False, "status": "DISABLED"},
            "vector_mode": "off",
        },
    )


def _global_index(knowledge: Path, marker: str | None) -> None:
    if marker is not None:
        source = knowledge / "verified" / "fact.md"
        source.parent.mkdir(parents=True, exist_ok=True)
        source.write_text(marker, encoding="utf-8")
    cache = knowledge / "global-cache"
    cache.mkdir(parents=True, exist_ok=True)
    chunks, stats = scan_global(knowledge, DEFAULT_CONFIG)
    build_lexical(cache / "lexical.sqlite3", chunks)
    generation = chunks_fingerprint(chunks)
    atomic_json(
        cache / "manifest.json",
        {
            "config_fingerprint": config_fingerprint(DEFAULT_CONFIG),
            "generation": generation,
            "indexed_at": utc_now(),
            "schema_version": SCHEMA_VERSION,
            "source_fingerprint": generation,
            "stats": stats,
            "vector": {"enabled": False, "status": "DISABLED"},
            "vector_mode": "off",
        },
    )


def _replace_tree(
    knowledge: Path,
    extra: tuple[LibraryNode, ...],
    edges: tuple[tuple[str, str], ...],
) -> KnowledgeTree:
    store = KnowledgeTreeStore(knowledge / TREE_FILE)
    current = store.bootstrap(knowledge / "catalog.json")
    by_id = {node.node_id: node for node in current.nodes}
    by_id.update({node.node_id: node for node in extra})
    replacement = KnowledgeTree(
        by_id.values(),
        edges,
        revision=current.revision + 1,
    )
    return store.save(replacement, expected_revision=current.revision)


def _query(
    knowledge: Path,
    project: Path,
    project_id: str,
    marker: str,
) -> dict[str, object]:
    return query_global(
        knowledge,
        project,
        marker,
        f"session-{project_id}",
        project_id,
        5,
    )


def test_nearest_hit_stops_before_global_and_sibling(tmp_path: Path) -> None:
    current, current_id = _project(tmp_path, "current")
    sibling, sibling_id = _project(tmp_path, "sibling")
    knowledge = tmp_path / "knowledge"
    _track(knowledge, [current, sibling])
    _replace_tree(
        knowledge,
        (LibraryNode("team", "Team", "group"),),
        (
            ("global", "team"),
            ("team", current_id),
            ("global", sibling_id),
        ),
    )
    _node_index(knowledge / "libraries" / "team", "NEAREST_MARKER")
    _global_index(knowledge, "NEAREST_MARKER")

    result = _query(knowledge, current, current_id, "NEAREST_MARKER")

    assert result["status"] == "GLOBAL_FALLBACK_HIT"
    assert result["hit_node"] == "team"
    assert result["query_chain"] == [current_id, "team", "global"]
    assert result["visited"] == [current_id, "team"]
    assert sibling_id not in result["query_chain"]


def test_miss_never_visits_sibling_or_descendant(tmp_path: Path) -> None:
    current, current_id = _project(tmp_path, "current")
    sibling, sibling_id = _project(tmp_path, "sibling")
    child, child_id = _project(tmp_path, "child")
    knowledge = tmp_path / "knowledge"
    _track(knowledge, [current, sibling, child])
    _replace_tree(
        knowledge,
        (),
        (
            ("global", current_id),
            ("global", sibling_id),
            (current_id, child_id),
        ),
    )
    _node_index(
        knowledge / "projects" / sibling_id,
        "FORBIDDEN_BRANCH_MARKER",
        path="project/fact.md",
    )
    _node_index(
        knowledge / "projects" / child_id,
        "FORBIDDEN_BRANCH_MARKER",
        path="project/fact.md",
    )
    _global_index(knowledge, None)

    result = _query(
        knowledge, current, current_id, "FORBIDDEN_BRANCH_MARKER"
    )

    assert result["status"] == "KNOWLEDGE_MISS"
    assert result["visited"] == [current_id, "global"]
    assert sibling_id not in result["query_chain"]
    assert child_id not in result["query_chain"]


def test_global_has_no_privilege_and_can_be_child(tmp_path: Path) -> None:
    current, current_id = _project(tmp_path, "current")
    knowledge = tmp_path / "knowledge"
    _track(knowledge, [current])
    _replace_tree(
        knowledge,
        (LibraryNode("parent", "Parent", "group"),),
        (("parent", "global"), ("global", current_id)),
    )
    _global_index(knowledge, "GLOBAL_CHILD_MARKER")

    result = _query(
        knowledge, current, current_id, "GLOBAL_CHILD_MARKER"
    )

    assert result["query_chain"] == [current_id, "global", "parent"]
    assert result["visited"] == [current_id, "global"]
    assert result["hit_node"] == "global"


def test_mounted_and_group_unavailable_are_explicit(tmp_path: Path) -> None:
    current, current_id = _project(tmp_path, "current")
    knowledge = tmp_path / "knowledge"
    _track(knowledge, [current])
    _replace_tree(
        knowledge,
        (
            LibraryNode("team", "Team", "group"),
            LibraryNode(
                "external",
                "External",
                "mounted",
                "mount-one",
                "READ_ONLY",
            ),
        ),
        (
            ("global", "team"),
            ("team", "external"),
            ("external", current_id),
        ),
    )
    _global_index(knowledge, None)

    result = _query(knowledge, current, current_id, "MISSING")

    unavailable = {
        item["node_id"]: item["reason"]
        for item in result["unavailable_nodes"]
    }
    assert unavailable == {
        "external": "MOUNT_CAPABILITY_UNSUPPORTED",
        "team": "INDEX_NOT_CONFIGURED",
    }
    assert result["visited"] == [
        current_id, "external", "team", "global"
    ]


def test_ancestor_project_hit_caches_only_current_leaf(tmp_path: Path) -> None:
    current, current_id = _project(tmp_path, "current")
    parent, parent_id = _project(tmp_path, "parent")
    sibling, sibling_id = _project(tmp_path, "sibling")
    knowledge = tmp_path / "knowledge"
    _track(knowledge, [current, parent, sibling])
    _replace_tree(
        knowledge,
        (),
        (
            ("global", parent_id),
            (parent_id, current_id),
            ("global", sibling_id),
        ),
    )
    source = knowledge / "verified" / "shared.md"
    source.parent.mkdir(parents=True)
    source.write_text("ANCESTOR_MARKER", encoding="utf-8")
    _node_index(
        knowledge / "projects" / parent_id,
        "ANCESTOR_MARKER",
        path="verified/shared.md",
    )

    result = _query(knowledge, current, current_id, "ANCESTOR_MARKER")

    assert result["hit_node"] == parent_id
    assert list_prefetch(knowledge / "projects" / current_id)
    assert not list_prefetch(knowledge / "projects" / parent_id)
    assert not list_prefetch(knowledge / "projects" / sibling_id)


def test_corrupt_tree_fails_closed_before_query(tmp_path: Path) -> None:
    current, current_id = _project(tmp_path, "current")
    knowledge = tmp_path / "knowledge"
    _track(knowledge, [current])
    KnowledgeTreeStore(knowledge / TREE_FILE).bootstrap(
        knowledge / "catalog.json"
    )
    (knowledge / TREE_FILE).write_text("{}", encoding="utf-8")

    with pytest.raises(TreeStoreError):
        _query(knowledge, current, current_id, "ANY")
