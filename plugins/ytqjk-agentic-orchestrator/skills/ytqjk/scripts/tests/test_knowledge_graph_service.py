from __future__ import annotations

import sys
from pathlib import Path


SKILL_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SKILL_ROOT / "dashboard"))
sys.path.insert(0, str(SKILL_ROOT / "scripts"))

from knowledge_graph_service import (  # noqa: E402
    build_knowledge_graph,
    explore_path,
    recommend_knowledge,
    semantic_search,
)


def _write_approved(root: Path, name: str, content: str) -> None:
    target = root / "personal-experience" / "approved" / name
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(content, encoding="utf-8")


def _fixture(root: Path) -> None:
    _write_approved(
        root,
        "graph.md",
        """# 知识图谱

[[知识图谱]] 使用 [[实体关系抽取]] 识别概念。
[[知识图谱]] 依赖 [[向量索引]] 完成语义召回。
""",
    )
    _write_approved(
        root,
        "search.md",
        """# 语义搜索

语义搜索基于 `向量索引` 与实体关系进行混合召回。
相似知识推荐复用知识图谱中的共享概念。
""",
    )
    _write_approved(
        root,
        "unrelated.md",
        """# 发布流程

发布流程包含构建、签名与部署步骤。
""",
    )


def _node_id(graph: dict[str, object], label: str) -> str:
    return next(
        node["id"]
        for node in graph["nodes"]
        if node["label"] == label
    )


def test_graph_extracts_entities_relations_and_provenance(
    tmp_path: Path,
) -> None:
    _fixture(tmp_path)

    result = build_knowledge_graph(tmp_path, limit=80)
    graph = result["graph"]

    labels = {node["label"] for node in graph["nodes"]}
    assert {"知识图谱", "实体关系抽取", "向量索引"} <= labels
    relation_types = {edge["type"] for edge in graph["edges"]}
    assert {"mentions", "uses", "depends_on"} <= relation_types
    explicit = next(
        edge for edge in graph["edges"] if edge["type"] == "depends_on"
    )
    assert explicit["evidence"][0]["path"] == (
        "personal-experience/approved/graph.md"
    )
    assert graph["capabilities"]["entity_extraction"] == "rules-v1"
    assert graph["capabilities"]["path_exploration"] is True


def test_semantic_search_and_similar_recommendation(tmp_path: Path) -> None:
    _fixture(tmp_path)

    search = semantic_search(tmp_path, "向量语义召回", limit=5)

    assert search["results"]
    assert search["results"][0]["path"].endswith("search.md")
    assert search["mode"] in {"concept-hybrid", "hybrid-vector"}
    graph = build_knowledge_graph(tmp_path, limit=80)["graph"]
    graph_doc = next(
        node for node in graph["nodes"]
        if node.get("path", "").endswith("graph.md")
    )
    recommendations = recommend_knowledge(
        tmp_path, graph_doc["id"], limit=5,
    )
    assert any(
        item.get("path", "").endswith("search.md")
        for item in recommendations["results"]
    )


def test_path_exploration_returns_ordered_nodes_and_edges(
    tmp_path: Path,
) -> None:
    _fixture(tmp_path)
    graph = build_knowledge_graph(tmp_path, limit=80)["graph"]
    source = _node_id(graph, "实体关系抽取")
    target = _node_id(graph, "向量索引")

    result = explore_path(tmp_path, source, target, max_depth=4)

    assert result["found"] is True
    assert result["nodes"][0]["id"] == source
    assert result["nodes"][-1]["id"] == target
    assert len(result["edges"]) == len(result["nodes"]) - 1


def test_queries_are_bounded_and_unknown_nodes_are_explicit(
    tmp_path: Path,
) -> None:
    _fixture(tmp_path)

    try:
        semantic_search(tmp_path, "", limit=5)
    except ValueError as exc:
        assert str(exc) == "EMPTY_QUERY"
    else:
        raise AssertionError("empty query must be rejected")

    result = recommend_knowledge(tmp_path, "missing", limit=5)
    assert result == {"node_id": "missing", "results": []}
    path = explore_path(tmp_path, "missing", "also-missing", max_depth=4)
    assert path["found"] is False
    assert path["reason"] == "UNKNOWN_NODE"
