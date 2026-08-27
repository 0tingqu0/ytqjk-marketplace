from __future__ import annotations

import sys
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402


def _catalog(*project_ids: str) -> dict[str, object]:
    return {
        "projects": {
            project_id: {"name": f"Project {project_id}"}
            for project_id in project_ids
        }
    }


def _personal(tree: KnowledgeTree) -> LibraryNode:
    return next(node for node in tree.nodes if node.node_id == "global")


def test_bootstrap_creates_personal_root_idempotently(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")

    first = store.bootstrap(_catalog("project-a"))
    second = store.bootstrap(_catalog("project-a"))

    assert first.revision == 0
    assert second.revision == 0
    assert _personal(second).title == "个人总库"
    assert second.edges == (("global", "project-a"),)


def test_bootstrap_migrates_only_legacy_personal_root_title(
    tmp_path: Path,
) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    legacy = KnowledgeTree((
        LibraryNode("global", "总知识库", "global"),
        LibraryNode("project-a", "Project project-a", "project"),
    ), (("global", "project-a"),))
    store.save(legacy, expected_revision=-1)

    changed = store.bootstrap(_catalog("project-a"))
    stable = store.bootstrap(_catalog("project-a"))

    assert changed.revision == 1
    assert stable.revision == 1
    assert _personal(stable).title == "个人总库"
    assert stable.edges == (("global", "project-a"),)


def test_bootstrap_preserves_custom_personal_root_title(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    custom = KnowledgeTree((LibraryNode("global", "我的知识根", "global"),))
    store.save(custom, expected_revision=-1)

    stable = store.bootstrap(_catalog())

    assert stable.revision == 0
    assert _personal(stable).title == "我的知识根"
