from __future__ import annotations

import json
import os
import sys
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "dashboard"))
sys.path.insert(0, str(ROOT / "scripts"))

from dashboard_tree_api import (  # noqa: E402
    DashboardTreeApi,
    DashboardTreeApiError,
)
from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402


def _node(node_id: str, kind: str = "group") -> LibraryNode:
    return LibraryNode(node_id, f"Node {node_id}", kind)


def _service(tmp_path: Path) -> DashboardTreeApi:
    nodes = (
        _node("global", "global"),
        _node("alpha", "project"),
        _node("leaf"),
        _node("other"),
        _node("bridge"),
        _node("orphan"),
    )
    tree = KnowledgeTree(
        nodes,
        (("global", "alpha"), ("alpha", "leaf")),
    )
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    store.save(tree, expected_revision=-1)
    return DashboardTreeApi(store)


def _commit(
    service: DashboardTreeApi,
    action: str,
    arguments: dict[str, object],
) -> dict[str, object]:
    issued = service.preview(action, arguments)["preview"]
    return service.commit(
        action,
        {
            "digest": issued["digest"],
            "expected_revision": issued["expected_revision"],
        },
    )


def _error(
    service: DashboardTreeApi,
    action: str,
    arguments: object,
) -> DashboardTreeApiError:
    with pytest.raises(DashboardTreeApiError) as captured:
        service.preview(action, arguments)
    return captured.value


def _by_id(snapshot: dict[str, object]) -> dict[str, dict[str, object]]:
    nodes = snapshot["tree"]["nodes"]
    return {node["id"]: node for node in nodes}


def test_snapshot_is_stable_json_without_internal_object_leak(
    tmp_path: Path,
) -> None:
    service = _service(tmp_path)
    first = service.snapshot()
    encoded = json.dumps(first, ensure_ascii=False, allow_nan=False)

    assert first["ok"] is True
    assert first["tree"]["revision"] == 0
    assert set(first["tree"]) == {
        "revision", "digest", "nodes", "edges", "roots",
    }
    assert '"parent_id": "global"' in encoded
    first["tree"]["nodes"][0]["id"] = "poisoned"
    first["tree"]["nodes"][0]["metadata"]["path"] = "../escape"

    second = service.snapshot()
    assert "poisoned" not in _by_id(second)
    assert all(
        "path" not in node["metadata"]
        for node in _by_id(second).values()
    )


def test_create_group_and_opaque_mounted_node(
    tmp_path: Path,
) -> None:
    service = _service(tmp_path)
    group = {
        "node_id": "team", "title": "Team", "type": "group",
        "parent_id": "global", "metadata": {},
    }
    mounted = {
        "node_id": "remote", "title": "Remote", "type": "mounted",
        "parent_id": "team",
        "metadata": {
            "mount_id": "connector-17", "capability": "READ_ONLY",
        },
    }

    first = _commit(service, "create", group)
    second = _commit(service, "create", mounted)
    nodes = _by_id(second)

    assert first["revision"] == 1
    assert second["revision"] == 2
    assert nodes["team"]["parent_id"] == "global"
    assert nodes["remote"]["metadata"] == mounted["metadata"]
    assert set(tmp_path.iterdir()) == {
        tmp_path / "tree.json", tmp_path / "tree.json.lock",
    }


def test_global_can_be_attached_as_ordinary_child(tmp_path: Path) -> None:
    service = _service(tmp_path)
    result = _commit(
        service, "attach", {"node_id": "global", "parent_id": "other"},
    )

    assert _by_id(result)["global"]["parent_id"] == "other"
    assert _by_id(result)["leaf"]["parent_id"] == "alpha"


def test_invalid_ids_types_metadata_and_extra_fields_fail_closed(
    tmp_path: Path,
) -> None:
    service = _service(tmp_path)
    base = {
        "node_id": "new", "title": "New", "type": "group",
        "parent_id": None, "metadata": {},
    }
    cases = (
        ({**base, "node_id": "../escape"}, "INVALID_NODE_ID"),
        ({**base, "type": "project"}, "CREATION_TYPE_FORBIDDEN"),
        ({**base, "type": []}, "CREATION_TYPE_FORBIDDEN"),
        ({**base, "metadata": {"path": "/private"}},
         "GROUP_METADATA_FORBIDDEN"),
        ({**base, "extra": True}, "INVALID_REQUEST_FIELDS"),
    )
    for payload, code in cases:
        error = _error(service, "create", payload)
        assert error.status == 400
        assert error.code == code

    unsafe = {
        **base, "type": "mounted", "title": "https://private.example",
        "metadata": {"mount_id": "mount-1", "capability": "READ_ONLY"},
    }
    error = _error(service, "create", unsafe)
    assert error.status == 400
    assert error.code == "UNSAFE_MOUNT_METADATA"


def test_unknown_self_parent_and_cycle_have_explicit_status(
    tmp_path: Path,
) -> None:
    service = _service(tmp_path)
    unknown = _error(service, "detach", {"node_id": "missing"})
    self_parent = _error(
        service, "attach", {"node_id": "other", "parent_id": "other"},
    )
    cycle = _error(
        service, "attach", {"node_id": "global", "parent_id": "leaf"},
    )
    missing_parent = _error(
        service,
        "create",
        {
            "node_id": "new", "title": "New", "type": "group",
            "parent_id": "missing", "metadata": {},
        },
    )

    assert (unknown.status, unknown.code) == (404, "UNKNOWN_NODE")
    assert (self_parent.status, self_parent.code) == (409, "SELF_PARENT")
    assert (cycle.status, cycle.code) == (409, "CYCLE_DETECTED")
    assert (missing_parent.status, missing_parent.code) == (
        404, "UNKNOWN_NODE",
    )


def test_preview_is_one_time_and_stale_revision_is_rejected(
    tmp_path: Path,
) -> None:
    service = _service(tmp_path)
    first = service.preview(
        "attach", {"node_id": "orphan", "parent_id": "alpha"},
    )["preview"]
    stale = service.preview(
        "attach", {"node_id": "bridge", "parent_id": "alpha"},
    )["preview"]
    commit = {
        "digest": first["digest"],
        "expected_revision": first["expected_revision"],
    }
    service.commit("attach", commit)

    with pytest.raises(DashboardTreeApiError) as captured:
        service.commit(
            "attach",
            {
                "digest": stale["digest"],
                "expected_revision": stale["expected_revision"],
            },
        )
    assert captured.value.code == "REVISION_CONFLICT"
    with pytest.raises(DashboardTreeApiError) as replay:
        service.commit("attach", commit)
    assert replay.value.code == "PREVIEW_NOT_FOUND"


def test_wrong_action_consumes_preview_and_forgery_fails(
    tmp_path: Path,
) -> None:
    service = _service(tmp_path)
    issued = service.preview(
        "detach", {"node_id": "alpha"},
    )["preview"]
    commit = {
        "digest": issued["digest"],
        "expected_revision": issued["expected_revision"],
    }
    with pytest.raises(DashboardTreeApiError) as mismatch:
        service.commit("attach", commit)
    assert mismatch.value.code == "PREVIEW_MISMATCH"
    with pytest.raises(DashboardTreeApiError) as consumed:
        service.commit("detach", commit)
    assert consumed.value.code == "PREVIEW_NOT_FOUND"
    with pytest.raises(DashboardTreeApiError) as forged:
        service.commit(
            "detach", {"digest": "0" * 64, "expected_revision": 0},
        )
    assert forged.value.code == "PREVIEW_NOT_FOUND"


def test_detach_preserves_descendant_subtree(tmp_path: Path) -> None:
    service = _service(tmp_path)
    issued = service.preview("detach", {"node_id": "alpha"})["preview"]

    assert issued["summary"]["subtree_size"] == 2
    assert {"alpha", "leaf"} <= set(issued["affected_nodes"])
    result = service.commit(
        "detach",
        {
            "digest": issued["digest"],
            "expected_revision": issued["expected_revision"],
        },
    )
    nodes = _by_id(result)
    assert nodes["alpha"]["parent_id"] is None
    assert nodes["leaf"]["parent_id"] == "alpha"


def test_move_preserves_descendant_subtree(tmp_path: Path) -> None:
    service = _service(tmp_path)
    result = _commit(
        service, "move", {"node_id": "alpha", "parent_id": "other"},
    )
    nodes = _by_id(result)

    assert nodes["alpha"]["parent_id"] == "other"
    assert nodes["leaf"]["parent_id"] == "alpha"


def test_same_revision_topology_change_invalidates_preview(
    tmp_path: Path,
) -> None:
    service = _service(tmp_path)
    issued = service.preview(
        "attach", {"node_id": "orphan", "parent_id": "alpha"},
    )["preview"]
    current = service.store.load()
    altered = KnowledgeTree(
        current.nodes,
        (("global", "alpha"), ("other", "leaf")),
        revision=current.revision,
    )
    service.store.load = lambda: altered  # type: ignore[method-assign]

    with pytest.raises(DashboardTreeApiError) as captured:
        service.commit(
            "attach",
            {
                "digest": issued["digest"],
                "expected_revision": issued["expected_revision"],
            },
        )
    assert captured.value.code == "TOPOLOGY_CHANGED"


def test_insert_between_replaces_only_selected_edge(tmp_path: Path) -> None:
    service = _service(tmp_path)
    result = _commit(
        service,
        "insert_between",
        {"parent_id": "global", "node_id": "alpha",
         "middle_id": "bridge"},
    )
    edges = {
        (edge["parent_id"], edge["child_id"])
        for edge in result["tree"]["edges"]
    }

    assert ("global", "bridge") in edges
    assert ("bridge", "alpha") in edges
    assert ("global", "alpha") not in edges


def test_error_payload_and_not_configured_are_serializable(
    tmp_path: Path,
) -> None:
    service = DashboardTreeApi(KnowledgeTreeStore(tmp_path / "missing.json"))
    with pytest.raises(DashboardTreeApiError) as captured:
        service.snapshot()

    error = captured.value
    assert error.status == 503
    assert error.code == "TREE_NOT_CONFIGURED"
    assert json.loads(json.dumps(error.payload()))["error"]["status"] == 503


def test_reparse_parent_is_rejected_when_platform_supports_it(
    tmp_path: Path,
) -> None:
    real = tmp_path / "real"
    real.mkdir()
    linked = tmp_path / "linked"
    try:
        os.symlink(real, linked, target_is_directory=True)
    except OSError:
        pytest.skip("directory symlink unavailable")
    service = DashboardTreeApi(KnowledgeTreeStore(linked / "tree.json"))

    with pytest.raises(DashboardTreeApiError) as captured:
        service.snapshot()
    assert captured.value.status == 503
    assert captured.value.code == "UNSAFE_REPARSE_PATH"
