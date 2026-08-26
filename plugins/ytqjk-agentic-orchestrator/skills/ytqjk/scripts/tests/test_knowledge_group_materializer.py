from __future__ import annotations

import hashlib
import sys
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_group_materializer import (  # noqa: E402
    GroupLibraryService,
    GroupMaterializationError,
)
from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402
from rag_common import load_json, read_chunks  # noqa: E402


def _store(root: Path) -> KnowledgeTreeStore:
    root.mkdir()
    tree = KnowledgeTree(
        (
            LibraryNode("global", "Global", "global"),
            LibraryNode("team", "Team", "group"),
            LibraryNode("project", "Project", "project"),
        ),
        (("global", "team"), ("team", "project")),
    )
    store = KnowledgeTreeStore(root / "tree.json")
    store.save(tree, expected_revision=-1)
    return store


def _write(root: Path, relative: str, content: str) -> Path:
    target = root / relative
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(content, encoding="utf-8")
    return target


def _hashes(directory: Path) -> dict[str, str]:
    return {
        path.relative_to(directory).as_posix(): hashlib.sha256(
            path.read_bytes()
        ).hexdigest()
        for path in directory.rglob("*")
        if path.is_file()
    }


def _untrusted_id(path: str) -> str:
    return hashlib.sha256(path.encode("utf-8")).hexdigest()


def test_rebuild_is_governed_and_idempotent(tmp_path: Path) -> None:
    root = tmp_path / "knowledge"
    store = _store(root)
    source = _write(root, "verified/fact.md", "GROUP_MARKER")
    _write(
        root,
        "personal-experience/candidates/draft.md",
        "CANDIDATE_MARKER",
    )
    service = GroupLibraryService(root, store)

    first = service.rebuild("team", 0)
    active = root / "libraries" / "team"
    before = _hashes(active)
    second = service.rebuild("team", 0)
    manifest = load_json(active / "manifest.json", {})
    chunks = read_chunks(active / "lexical.sqlite3")

    assert first["status"] == "REBUILT"
    assert second["status"] == "REUSED"
    assert _hashes(active) == before
    assert first["documents"] == 1
    assert [item["path"] for item in manifest["documents"]] == [
        "verified/fact.md"
    ]
    assert chunks[0].source_sha256 == hashlib.sha256(
        source.read_text(encoding="utf-8").encode("utf-8")
    ).hexdigest()

    with pytest.raises(GroupMaterializationError) as candidate:
        service.rebuild(
            "team",
            0,
            [_untrusted_id(
                "personal-experience/candidates/draft.md"
            )],
        )
    assert candidate.value.code == "UNKNOWN_DOCUMENT_ID"
    with pytest.raises(GroupMaterializationError) as outside:
        service.rebuild(
            "team", 0, [_untrusted_id("../outside.md")]
        )
    assert outside.value.code == "UNKNOWN_DOCUMENT_ID"


def test_switch_failure_restores_old_index(tmp_path: Path) -> None:
    root = tmp_path / "knowledge"
    store = _store(root)
    source = _write(root, "verified/fact.md", "OLD_MARKER")
    service = GroupLibraryService(root, store)
    service.rebuild("team", 0)
    active = root / "libraries" / "team"
    before = _hashes(active)
    source.write_text("NEW_MARKER", encoding="utf-8")
    original = service.storage.readback

    def fail_active(
        directory: Path,
        verify_sources: bool,
    ) -> dict[str, object]:
        if directory == active:
            raise GroupMaterializationError(
                503, "INJECTED_READBACK_FAILURE"
            )
        return original(directory, verify_sources)

    service.storage.readback = fail_active  # type: ignore[method-assign]
    with pytest.raises(GroupMaterializationError):
        service.rebuild("team", 0)

    assert _hashes(active) == before
    assert read_chunks(active / "lexical.sqlite3")[0].content == (
        "OLD_MARKER"
    )
    with pytest.raises(GroupMaterializationError) as stale:
        GroupLibraryService(root, store).rebuild("team", 1)
    assert stale.value.code == "REVISION_CONFLICT"


def test_source_change_during_build_keeps_old(tmp_path: Path) -> None:
    root = tmp_path / "knowledge"
    store = _store(root)
    source = _write(root, "verified/fact.md", "FIRST_MARKER")
    GroupLibraryService(root, store).rebuild("team", 0)
    active = root / "libraries" / "team"
    before = _hashes(active)
    source.write_text("SECOND_MARKER", encoding="utf-8")

    def drift() -> None:
        source.write_text("THIRD_MARKER", encoding="utf-8")

    service = GroupLibraryService(root, store, before_switch=drift)
    with pytest.raises(GroupMaterializationError) as captured:
        service.rebuild("team", 0)

    assert captured.value.code == "SOURCE_CHANGED_DURING_BUILD"
    assert _hashes(active) == before


def test_status_rejects_unsafe_node_id_without_side_effects(
    tmp_path: Path,
) -> None:
    root = tmp_path / "knowledge"
    service = GroupLibraryService(root, _store(root))

    assert service.status("../outside") == {"status": "CORRUPT"}
    assert not (tmp_path / "outside").exists()
