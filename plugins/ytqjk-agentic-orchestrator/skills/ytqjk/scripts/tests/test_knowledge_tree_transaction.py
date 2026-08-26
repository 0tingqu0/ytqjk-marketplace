from __future__ import annotations

import sys
import threading
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import global_session_query  # noqa: E402
from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_runtime import TREE_FILE  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402
from project_prefetch import list_prefetch  # noqa: E402
from project_tracking import identify_project, track_project  # noqa: E402
from rag_test_support import index_global, make_repo  # noqa: E402


def _query(
    knowledge: Path,
    project: Path,
    project_id: str,
    text: str,
) -> dict[str, object]:
    return global_session_query.query_global(
        knowledge,
        project,
        text,
        "tree-transaction-session",
        project_id,
        5,
    )


def _detach_current(
    store: KnowledgeTreeStore,
    project_id: str,
    attempted: threading.Event,
    completed: threading.Event,
    errors: list[BaseException],
) -> None:
    attempted.set()
    try:
        tree = store.load()
        preview = tree.preview_detach(project_id)
        tree.detach(
            project_id,
            preview=preview,
            expected_revision=tree.revision,
        )
        store.save(tree, expected_revision=preview.base_revision)
    except BaseException as error:
        errors.append(error)
    finally:
        completed.set()


def test_read_transaction_blocks_writer_until_exit(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    tree = store.bootstrap(
        {"projects": {"project-a": {"name": "Project A"}}}
    )
    preview = tree.preview_detach("project-a")
    tree.detach(
        "project-a",
        preview=preview,
        expected_revision=tree.revision,
    )
    attempted = threading.Event()
    completed = threading.Event()
    errors: list[BaseException] = []

    def save_tree() -> None:
        attempted.set()
        try:
            store.save(tree, expected_revision=preview.base_revision)
        except BaseException as error:
            errors.append(error)
        finally:
            completed.set()

    writer = threading.Thread(target=save_tree, daemon=True)
    with store.read_transaction() as snapshot:
        writer.start()
        assert attempted.wait(1)
        assert not completed.wait(0.1)
        assert snapshot.revision == 0
    assert completed.wait(2)
    writer.join(2)
    assert not errors
    assert store.load().revision == 1


def test_query_holds_tree_lock_across_ancestor_io_and_cache(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    project = make_repo(tmp_path / "project")
    project_id = identify_project(project)["id"]
    knowledge = tmp_path / "knowledge"
    source = knowledge / "verified" / "fact.md"
    source.parent.mkdir(parents=True)
    source.write_text("TREE_LOCK_MARKER", encoding="utf-8")
    index_global(knowledge)
    track_project(knowledge, project)
    store = KnowledgeTreeStore(knowledge / TREE_FILE)
    store.bootstrap(knowledge / "catalog.json")
    attempted = threading.Event()
    completed = threading.Event()
    errors: list[BaseException] = []
    writer = threading.Thread(
        target=_detach_current,
        args=(store, project_id, attempted, completed, errors),
        daemon=True,
    )
    original = global_session_query._query_index
    calls = 0

    def blocked_writer_query(*args: object, **kwargs: object) -> object:
        nonlocal calls
        calls += 1
        if calls == 1:
            writer.start()
            assert attempted.wait(1)
            assert not completed.wait(0.1)
        return original(*args, **kwargs)

    monkeypatch.setattr(
        global_session_query, "_query_index", blocked_writer_query
    )
    first = _query(knowledge, project, project_id, "TREE_LOCK_MARKER")
    assert completed.wait(2)
    writer.join(2)

    assert not errors
    assert first["status"] == "GLOBAL_FALLBACK_HIT"
    assert first["query_chain"] == [project_id, "global"]
    assert list_prefetch(knowledge / "projects" / project_id)
    second = _query(knowledge, project, project_id, "TREE_LOCK_MARKER")
    assert second["status"] == "KNOWLEDGE_MISS"
    assert second["query_chain"] == [project_id]
    assert not list_prefetch(knowledge / "projects" / project_id)


def test_query_exception_releases_tree_transaction(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    project = make_repo(tmp_path / "project")
    project_id = identify_project(project)["id"]
    knowledge = tmp_path / "knowledge"
    track_project(knowledge, project)
    store = KnowledgeTreeStore(knowledge / TREE_FILE)
    tree = store.bootstrap(knowledge / "catalog.json")

    def fail_query(*args: object, **kwargs: object) -> object:
        raise ValueError("EXPECTED_QUERY_FAILURE")

    monkeypatch.setattr(global_session_query, "_query_index", fail_query)
    with pytest.raises(ValueError, match="EXPECTED_QUERY_FAILURE"):
        _query(knowledge, project, project_id, "ANY")
    preview = tree.preview_detach(project_id)
    tree.detach(
        project_id,
        preview=preview,
        expected_revision=tree.revision,
    )
    saved = store.save(tree, expected_revision=preview.base_revision)
    assert saved.ancestors(project_id) == (project_id,)


def test_persisted_node_loss_is_not_bootstrapped(tmp_path: Path) -> None:
    project = tmp_path / "project"
    project.mkdir()
    project_id = identify_project(project)["id"]
    knowledge = tmp_path / "knowledge"
    track_project(knowledge, project)
    store = KnowledgeTreeStore(knowledge / TREE_FILE)
    tree = store.bootstrap(knowledge / "catalog.json")
    broken = KnowledgeTree(
        (LibraryNode("global", "Global", "global"),),
        revision=tree.revision + 1,
    )
    store.save(broken, expected_revision=tree.revision)

    with pytest.raises(
        RuntimeError, match="CURRENT_PROJECT_TREE_NODE_MISSING"
    ):
        _query(knowledge, project, project_id, "ANY")
    assert store.load().ancestors("global") == ("global",)
