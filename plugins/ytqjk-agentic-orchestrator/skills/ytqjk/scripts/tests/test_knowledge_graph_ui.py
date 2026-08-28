from __future__ import annotations

import json
import shutil
import subprocess
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).resolve().parents[2]
DASHBOARD = SKILL_ROOT / "dashboard"


def test_workbench_exposes_search_recommendation_and_path_controls() -> None:
    html = (DASHBOARD / "index.html").read_text(encoding="utf-8")
    api = (DASHBOARD / "js" / "api.js").read_text(encoding="utf-8")
    workbench = (
        DASHBOARD / "js" / "views" / "knowledge-graph-workbench.js"
    ).read_text(encoding="utf-8")

    for identifier in (
        "graph-search-input", "graph-inspector", "graph-recommendations",
        "graph-set-source", "graph-set-target", "graph-run-path",
    ):
        assert f'id="{identifier}"' in html
    assert 'data-graph-mode="semantic"' in html
    assert 'data-graph-mode="topology"' in html
    assert "semanticSearch:" in api
    assert "knowledgeRecommendations:" in api
    assert "knowledgePath:" in api
    assert "applyGraphHighlight" in workbench


def test_graph_ui_has_keyboard_and_reduced_motion_support() -> None:
    renderer = (
        DASHBOARD / "js" / "views" / "semantic-graph-render.js"
    ).read_text(encoding="utf-8")
    css = (DASHBOARD / "knowledge-graph-workbench.css").read_text(
        encoding="utf-8"
    )

    assert 'tabindex: "0"' in renderer
    assert 'event.key === "Enter" || event.key === " "' in renderer
    assert "@media (prefers-reduced-motion: reduce)" in css
    assert ".semantic-edge--similar_to" in css


def test_force_layout_produces_finite_coordinates() -> None:
    node = shutil.which("node")
    if node is None:
        pytest.skip("Node.js is unavailable")
    module = (DASHBOARD / "js" / "views" / "knowledge-graph-layout.js")
    graph = {
        "nodes": [
            {"id": "doc:a", "type": "document"},
            {"id": "entity:b", "type": "entity"},
        ],
        "edges": [
            {"source": "doc:a", "target": "entity:b", "type": "mentions"},
        ],
    }
    script = (
        f'import {{ layoutKnowledgeGraph }} from {json.dumps(module.as_uri())};'
        f"const result = layoutKnowledgeGraph({json.dumps(graph)});"
        "const values = [...result.positions.values()].flatMap(Object.values);"
        "if (!values.every(Number.isFinite)) process.exit(2);"
    )

    completed = subprocess.run(
        [node, "--input-type=module", "--eval", script],
        check=False,
        capture_output=True,
        text=True,
        timeout=15,
    )

    assert completed.returncode == 0, completed.stderr
