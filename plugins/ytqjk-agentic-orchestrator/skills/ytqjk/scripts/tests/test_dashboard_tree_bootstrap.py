from __future__ import annotations

import json
import sys
import threading
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "dashboard"))
sys.path.insert(0, str(ROOT / "scripts"))

from dashboard_tree_http import tree_service  # noqa: E402
from knowledge_tree import KnowledgeTree, LibraryNode  # noqa: E402
from knowledge_tree_store import KnowledgeTreeStore  # noqa: E402


def _handler(root: Path) -> object:
    return type(
        "TreeBootstrapHandler",
        (),
        {
            "knowledge_root": root,
            "tree_api": None,
            "update_lock": threading.Lock(),
        },
    )()


def test_missing_tree_bootstraps_global_and_catalog_projects(
    tmp_path: Path,
) -> None:
    catalog = {
        "projects": {
            "alpha--123": {"name": "Alpha"},
            "beta--456": {"name": "Beta"},
        },
    }
    (tmp_path / "catalog.json").write_text(
        json.dumps(catalog), encoding="utf-8",
    )

    tree = tree_service(_handler(tmp_path)).snapshot()["tree"]
    nodes = {node["id"]: node for node in tree["nodes"]}

    assert set(nodes) == {"global", "alpha--123", "beta--456"}
    assert nodes["global"]["title"] == "个人总库"
    assert nodes["alpha--123"]["parent_id"] == "global"
    assert nodes["beta--456"]["parent_id"] == "global"
    assert (tmp_path / "tree.json").is_file()


def test_missing_catalog_bootstraps_empty_global_tree(tmp_path: Path) -> None:
    tree = tree_service(_handler(tmp_path)).snapshot()["tree"]

    assert tree["roots"] == ["global"]
    assert [node["id"] for node in tree["nodes"]] == ["global"]


def test_existing_tree_migrates_personal_root_title(tmp_path: Path) -> None:
    store = KnowledgeTreeStore(tmp_path / "tree.json")
    store.save(
        KnowledgeTree((LibraryNode("global", "总知识库", "global"),)),
        expected_revision=-1,
    )

    tree = tree_service(_handler(tmp_path)).snapshot()["tree"]

    assert tree["revision"] == 1
    assert tree["nodes"][0]["title"] == "个人总库"
