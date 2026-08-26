from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import sys

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import knowledge_tree_store as store_module  # noqa: E402
from knowledge_tree import (  # noqa: E402
    KnowledgeTree,
    LibraryNode,
    MAX_REVISION,
    RevisionConflict,
)
from knowledge_tree_store import (  # noqa: E402
    KnowledgeTreeStore,
    TreeStoreError,
)


def _catalog(*project_ids: str) -> dict[str, object]:
    return {
        "projects": {
            project_id: {"name": f"Project {project_id}"}
            for project_id in project_ids
        }
    }


def _node(node_id: str, kind: str = "group") -> LibraryNode:
    return LibraryNode(node_id, f"Node {node_id}", kind)


def _write_signed(
    store: KnowledgeTreeStore, payload: dict[str, object],
) -> None:
    body = {key: value for key, value in payload.items() if key != "digest"}
    payload["digest"] = store_module._digest(body)
    store.path.write_text(
        json.dumps(payload, ensure_ascii=False), encoding="utf-8",
    )


def test_bootstrap_creates_global_and_projects_idempotently(
    tmp_path: Path,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")

    first = store.bootstrap(_catalog("project-a", "project-b"))
    second = store.bootstrap(_catalog("project-a", "project-b"))

    assert first.revision == 0
    assert second.revision == 0
    assert {node.node_id for node in second.nodes} == {
        "global", "project-a", "project-b",
    }
    assert second.edges == (
        ("global", "project-a"),
        ("global", "project-b"),
    )


def test_bootstrap_adds_only_new_catalog_projects(
    tmp_path: Path,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    store.bootstrap(_catalog("project-a"))

    changed = store.bootstrap(_catalog("project-a", "project-b"))
    stable = store.bootstrap(_catalog("project-a", "project-b"))

    assert changed.revision == 1
    assert stable.revision == 1
    assert ("global", "project-b") in stable.edges


def test_bootstrap_updates_catalog_title_without_resetting_edges(
    tmp_path: Path,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    store.bootstrap({"projects": {"project-a": {"name": "Old"}}})

    changed = store.bootstrap(
        {"projects": {"project-a": {"name": "Renamed"}}},
    )

    project = next(node for node in changed.nodes
                   if node.node_id == "project-a")
    assert changed.revision == 1
    assert project.title == "Renamed"
    assert changed.edges == (("global", "project-a"),)


def test_catalog_path_is_strict_utf8_json(tmp_path: Path) -> None:
    catalog = tmp_path / "catalog.json"
    catalog.write_text(
        json.dumps(_catalog("project-a"), ensure_ascii=False),
        encoding="utf-8",
    )
    store = KnowledgeTreeStore(tmp_path / "tree.json")

    tree = store.bootstrap(catalog)

    assert tree.ancestors("project-a") == ("project-a", "global")

    catalog.write_bytes(b"\xff\xfe")
    with pytest.raises(TreeStoreError, match="JSON_NOT_UTF8"):
        store.bootstrap(catalog)


def test_global_can_be_reattached_and_persisted(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    tree = KnowledgeTree((_node("global", "global"), _node("group")))
    tree = store.save(tree, expected_revision=-1)
    preview = tree.preview_attach("global", "group")

    tree.attach("global", "group", preview=preview, expected_revision=0)
    saved = store.save(tree, expected_revision=0)

    assert saved.ancestors("global") == ("global", "group")


def test_store_cas_rejects_lost_update(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    initial = KnowledgeTree(
        (_node("root"), _node("first"), _node("second")),
    )
    store.save(initial, expected_revision=-1)
    first = store.load()
    second = store.load()
    first_preview = first.preview_attach("first", "root")
    second_preview = second.preview_attach("second", "root")
    first.attach(
        "first", "root", preview=first_preview, expected_revision=0,
    )
    second.attach(
        "second", "root", preview=second_preview, expected_revision=0,
    )

    store.save(first, expected_revision=0)
    with pytest.raises(RevisionConflict, match="REVISION_CONFLICT"):
        store.save(second, expected_revision=0)

    assert store.load().edges == (("root", "first"),)


def test_atomic_failure_preserves_previous_tree(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    tree = KnowledgeTree((_node("root"), _node("child")))
    store.save(tree, expected_revision=-1)
    preview = tree.preview_attach("child", "root")
    tree.attach("child", "root", preview=preview, expected_revision=0)
    original = store.path.read_bytes()

    def fail_replace(source: Path, target: Path) -> None:
        del source, target
        raise OSError("injected replace failure")

    monkeypatch.setattr(store_module.os, "replace", fail_replace)
    with pytest.raises(TreeStoreError, match="TREE_WRITE_FAILED"):
        store.save(tree, expected_revision=0)

    assert store.path.read_bytes() == original


def test_readback_failure_restores_existing_bytes(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    tree = store.bootstrap(_catalog("project-a"))
    original = store.path.read_bytes()
    preview = tree.preview_detach("project-a")
    tree.detach("project-a", preview=preview, expected_revision=0)

    def fail_readback() -> KnowledgeTree:
        raise RuntimeError("native details must not escape")

    monkeypatch.setattr(store, "_require_readback", fail_readback)
    with pytest.raises(TreeStoreError) as captured:
        store.save(tree, expected_revision=0)

    assert str(captured.value) == "TREE_READBACK_FAILED"
    assert store.path.read_bytes() == original
    monkeypatch.undo()
    assert store.load().revision == 0


def test_readback_failure_removes_first_new_file(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")

    def fail_readback() -> KnowledgeTree:
        raise RuntimeError("injected readback failure")

    monkeypatch.setattr(store, "_require_readback", fail_readback)
    with pytest.raises(TreeStoreError, match="TREE_READBACK_FAILED"):
        store.save(KnowledgeTree((_node("root"),)), expected_revision=-1)

    assert not store.path.exists()


@pytest.mark.parametrize(
    "payload, message",
    (
        (b"\xff", "JSON_NOT_UTF8"),
        (b"\xef\xbb\xbf{}", "UTF8_BOM_FORBIDDEN"),
        (b'{"digest":"a","digest":"b"}', "DUPLICATE_JSON_KEY"),
        (b'{"schema_version":NaN}', "INVALID_JSON_NUMBER"),
        (b'{"value":1e309}', "INVALID_JSON_NUMBER"),
        (b'{"value":' + b"9" * 100 + b"}",
         "JSON_INTEGER_OUT_OF_RANGE"),
    ),
)
def test_store_rejects_noncanonical_json(
    tmp_path: Path, payload: bytes, message: str,
) -> None:
    path = tmp_path / "tree.json"
    path.write_bytes(payload)

    with pytest.raises(TreeStoreError) as captured:
        KnowledgeTreeStore(path).load()
    assert str(captured.value) == message


@pytest.mark.parametrize("schema_version", (True, 1.0))
def test_schema_version_requires_exact_integer(
    tmp_path: Path, schema_version: object,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    store.bootstrap(_catalog())
    payload = json.loads(store.path.read_text(encoding="utf-8"))
    payload["schema_version"] = schema_version
    _write_signed(store, payload)

    with pytest.raises(TreeStoreError, match="INVALID_TREE_DOCUMENT"):
        store.load()


def test_bootstrap_refuses_revision_overflow(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    store.bootstrap(_catalog())
    payload = json.loads(store.path.read_text(encoding="utf-8"))
    payload["revision"] = MAX_REVISION
    _write_signed(store, payload)

    with pytest.raises(RevisionConflict, match="REVISION_EXHAUSTED"):
        store.bootstrap(_catalog("project-a"))


def test_digest_tamper_is_detected(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    store.bootstrap(_catalog("project-a"))
    payload = json.loads(store.path.read_text(encoding="utf-8"))
    valid_digest, payload["digest"] = payload["digest"], "一" * 64
    store.path.write_text(json.dumps(payload), encoding="utf-8")
    with pytest.raises(TreeStoreError, match="INVALID_TREE_DIGEST"):
        store.load()
    payload["digest"] = valid_digest
    payload["revision"] = 99
    store.path.write_text(json.dumps(payload), encoding="utf-8")

    with pytest.raises(TreeStoreError, match="TREE_DIGEST_MISMATCH"):
        store.load()


def test_mounted_payload_has_no_connection_or_credential_fields(
    tmp_path: Path,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    mounted = LibraryNode(
        "mounted-1", "External", "mounted", "mount-1", "READ_ONLY",
    )
    store.save(KnowledgeTree((mounted,)), expected_revision=-1)

    payload = json.loads(store.path.read_text(encoding="utf-8"))
    record = payload["nodes"][0]
    assert set(record) == {
        "capability", "kind", "mount_id", "node_id", "title",
    }
    serialized = json.dumps(payload).casefold()
    assert "path" not in serialized
    assert "http" not in serialized
    assert "credential" not in serialized
    assert "password" not in serialized
    assert "token" not in serialized


def test_tree_file_symlink_is_rejected_without_touching_target(
    tmp_path: Path,
) -> None:
    outside = tmp_path / "outside.json"
    outside.write_text("OUTSIDE", encoding="utf-8")
    link = tmp_path / "tree.json"
    try:
        link.symlink_to(outside)
    except OSError as error:
        pytest.skip(f"symlink unavailable: {error}")

    with pytest.raises(TreeStoreError, match="UNSAFE_REPARSE_PATH"):
        KnowledgeTreeStore(link).load()

    assert outside.read_text(encoding="utf-8") == "OUTSIDE"


@pytest.mark.parametrize("location", ("file", "parent"))
def test_reparse_guard_has_deterministic_file_and_parent_coverage(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    location: str,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    store.bootstrap(_catalog())
    blocked = store.path if location == "file" else store.path.parent
    real_is_reparse = store_module.is_reparse

    def fake_is_reparse(candidate: Path) -> bool:
        if candidate.absolute() == blocked.absolute():
            return True
        return real_is_reparse(candidate)

    monkeypatch.setattr(store_module, "is_reparse", fake_is_reparse)
    with pytest.raises(TreeStoreError, match="UNSAFE_REPARSE_PATH"):
        store.load()


def test_invalid_next_revision_is_rejected(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    tree = KnowledgeTree((_node("root"),), revision=2)

    with pytest.raises(RevisionConflict, match="INVALID_NEXT_REVISION"):
        store.save(tree, expected_revision=-1)

    assert not store.path.exists()


def test_catalog_global_collision_is_rejected(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    with pytest.raises(TreeStoreError, match="CATALOG_NODE_CONFLICT"):
        store.bootstrap(_catalog("global"))


def test_catalog_project_ids_must_be_exact_strings(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    catalog = {"projects": {1: {"name": "forged"}, "valid": {}}}
    with pytest.raises(TreeStoreError, match="INVALID_CATALOG_PROJECT"):
        store.bootstrap(catalog)


@pytest.mark.skipif(os.name != "nt", reason="Windows junction regression")
def test_tree_parent_junction_is_rejected(tmp_path: Path) -> None:
    outside = tmp_path / "outside"
    outside.mkdir()
    link = tmp_path / "linked"
    result = subprocess.run(
        ["cmd.exe", "/d", "/c", "mklink", "/J", str(link), str(outside)],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        pytest.skip("junction unavailable")

    store = KnowledgeTreeStore(link / "tree.json")
    with pytest.raises(TreeStoreError, match="UNSAFE_REPARSE_PATH"):
        store.bootstrap(_catalog())
    assert not (outside / "tree.json").exists()


@pytest.mark.skipif(os.name == "nt", reason="covered by symlink test")
def test_tree_parent_symlink_is_rejected(tmp_path: Path) -> None:
    outside = tmp_path / "outside"
    outside.mkdir()
    link = tmp_path / "linked"
    link.symlink_to(outside, target_is_directory=True)

    store = KnowledgeTreeStore(link / "tree.json")
    with pytest.raises(TreeStoreError, match="UNSAFE_REPARSE_PATH"):
        store.bootstrap(_catalog())
