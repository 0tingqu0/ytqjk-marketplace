from __future__ import annotations

import json
import re
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
    accessibility = (
        DASHBOARD / "js" / "views" / "knowledge-graph-accessibility.js"
    ).read_text(encoding="utf-8")
    css = (DASHBOARD / "knowledge-graph-workbench.css").read_text(
        encoding="utf-8"
    )

    assert 'tabindex: "0"' in renderer
    assert 'event.key === "Enter" || event.key === " "' in accessibility
    assert "@media (prefers-reduced-motion: reduce)" in css
    assert ".semantic-edge--similar_to" in css


def test_graph_ui_exposes_bounded_zoom_pan_and_reset_controls() -> None:
    html = (DASHBOARD / "index.html").read_text(encoding="utf-8")
    renderer = (
        DASHBOARD / "js" / "views" / "semantic-graph-render.js"
    ).read_text(encoding="utf-8")
    viewport = (
        DASHBOARD / "js" / "views" / "knowledge-graph-viewport.js"
    ).read_text(encoding="utf-8")

    for identifier in (
        "graph-zoom-in", "graph-zoom-out", "graph-zoom-reset",
        "graph-zoom-level",
    ):
        assert f'id="{identifier}"' in html
    assert "bindGraphViewport" in renderer
    assert 'addEventListener("wheel"' in viewport
    assert 'addEventListener("pointermove"' in viewport
    assert "reset" in viewport


def test_graph_zoom_controls_do_not_cover_mobile_navigation() -> None:
    base_css = (DASHBOARD / "style.css").read_text(encoding="utf-8")
    graph_css = (DASHBOARD / "knowledge-graph-workbench.css").read_text(
        encoding="utf-8"
    )

    bottom_nav = re.search(
        r"\.bottom-nav\s*\{[^}]*z-index:\s*(\d+)", base_css
    )
    zoom_controls = re.search(
        r"\.graph-viewport-controls\s*\{[^}]*z-index:\s*(\d+)", graph_css
    )

    assert bottom_nav is not None
    assert zoom_controls is not None
    assert int(zoom_controls.group(1)) < int(bottom_nav.group(1))


def test_graph_workbench_invalidates_stale_selection_and_path_requests() -> None:
    workbench = (
        DASHBOARD / "js" / "views" / "knowledge-graph-workbench.js"
    ).read_text(encoding="utf-8")

    assert "reconcileGraphState" in workbench
    assert "pathRequest" in workbench
    assert "requestId !== runtime.pathRequest" in workbench


def test_refresh_policy_skips_unchanged_graph_and_hidden_pages() -> None:
    node = shutil.which("node")
    if node is None:
        pytest.skip("Node.js is unavailable")
    module = DASHBOARD / "js" / "refresh-policy.js"
    script = (
        f'import {{ loadKnowledgeGraph, sameData, shouldAutoRefresh }} '
        f'from {json.dumps(module.as_uri())};'
        "let revisions=0,graphs=0;"
        "const api={"
        "knowledgeGraphRevision:async()=>{revisions+=1;return{revision:'same'}},"
        "knowledgeGraph:async()=>{graphs+=1;return{revision:'new',graph:{nodes:[]}}}"
        "};"
        "const unchanged=await loadKnowledgeGraph(api,'same');"
        "if(unchanged.changed||revisions!==1||graphs!==0)process.exit(2);"
        "api.knowledgeGraphRevision=async()=>({revision:'new'});"
        "const changed=await loadKnowledgeGraph(api,'same');"
        "if(!changed.changed||graphs!==1||changed.revision!=='new')process.exit(3);"
        "if(!sameData({a:1},{a:1})||sameData({a:1},{a:2}))process.exit(4);"
        "if(shouldAutoRefresh(true)||!shouldAutoRefresh(false))process.exit(5);"
    )

    completed = subprocess.run(
        [node, "--input-type=module", "--eval", script],
        check=False,
        capture_output=True,
        text=True,
        timeout=15,
    )

    assert completed.returncode == 0, completed.stderr


def test_app_avoids_periodic_refresh_and_updates_after_visibility_change() -> None:
    app = (DASHBOARD / "app.js").read_text(encoding="utf-8")
    api = (DASHBOARD / "js" / "api.js").read_text(encoding="utf-8")

    assert "loadKnowledgeGraph" in app
    assert 'const clearedError = updateError("error", "");' in app
    assert "shouldAutoRefresh(document.hidden)" in app
    assert "refresh(true)" in app
    assert 'byId("refresh").onclick = () => refresh();' in app
    assert 'addEventListener("visibilitychange"' in app
    assert "setInterval" not in app
    assert "10_000" not in app
    assert "knowledgeGraphRevision:" in api


def test_graph_local_action_keeps_a_readable_full_row() -> None:
    styles = (DASHBOARD / "knowledge-graph-workbench.css").read_text(
        encoding="utf-8",
    )

    assert ".graph-endpoint-actions #graph-focus-local" in styles
    assert "grid-column: 1 / -1" in styles


def test_graph_zoom_math_stays_finite_and_bounded() -> None:
    node = shutil.which("node")
    if node is None:
        pytest.skip("Node.js is unavailable")
    module = (
        DASHBOARD / "js" / "views" / "knowledge-graph-viewport.js"
    )
    script = (
        f'import {{ zoomViewBox }} from {json.dumps(module.as_uri())};'
        "const base={x:0,y:0,width:900,height:570};"
        "const anchor={x:450,y:285};"
        "let value=base;"
        "for(let index=0;index<30;index+=1)"
        " value=zoomViewBox(value,0.8,anchor,base);"
        "if(value.width<299||value.height<189)process.exit(2);"
        "for(let index=0;index<60;index+=1)"
        " value=zoomViewBox(value,1.25,anchor,base);"
        "if(value.width>1351||value.height>856)process.exit(3);"
        "if(!Object.values(value).every(Number.isFinite))process.exit(4);"
    )

    completed = subprocess.run(
        [node, "--input-type=module", "--eval", script],
        check=False,
        capture_output=True,
        text=True,
        timeout=15,
    )

    assert completed.returncode == 0, completed.stderr


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


def test_graph_ui_exposes_obsidian_inspired_explorer_controls() -> None:
    html = (DASHBOARD / "index.html").read_text(encoding="utf-8")
    workbench = (
        DASHBOARD / "js" / "views" / "knowledge-graph-workbench.js"
    ).read_text(encoding="utf-8")

    for identifier in (
        "graph-settings-toggle", "graph-settings-panel",
        "graph-local-enabled", "graph-local-depth",
        "graph-show-documents", "graph-show-entities",
        "graph-show-labels", "graph-show-relations",
        "graph-node-scale", "graph-link-scale",
    ):
        assert f'id="{identifier}"' in html
    assert "bindGraphExplorer" in workbench


def test_local_graph_depth_follows_connected_nodes() -> None:
    node = shutil.which("node")
    if node is None:
        pytest.skip("Node.js is unavailable")
    module = DASHBOARD / "js" / "views" / "knowledge-graph-explorer.js"
    graph = {
        "nodes": [
            {"id": "a", "type": "document"},
            {"id": "b", "type": "entity"},
            {"id": "c", "type": "document"},
            {"id": "d", "type": "entity"},
        ],
        "edges": [
            {"id": "ab", "source": "a", "target": "b"},
            {"id": "bc", "source": "b", "target": "c"},
        ],
    }
    script = (
        f'import {{ graphNeighborhood }} from {json.dumps(module.as_uri())};'
        f"const graph={json.dumps(graph)};"
        "const one=graphNeighborhood(graph,'a',1);"
        "if([...one.nodeIds].sort().join(',')!=='a,b')process.exit(2);"
        "if([...one.edgeIds].join(',')!=='ab')process.exit(3);"
        "const two=graphNeighborhood(graph,'a',2);"
        "if([...two.nodeIds].sort().join(',')!=='a,b,c')process.exit(4);"
        "if([...two.edgeIds].sort().join(',')!=='ab,bc')process.exit(5);"
        "if(two.nodeIds.has('d'))process.exit(6);"
    )

    completed = subprocess.run(
        [node, "--input-type=module", "--eval", script],
        check=False,
        capture_output=True,
        text=True,
        timeout=15,
    )

    assert completed.returncode == 0, completed.stderr


def test_graph_viewport_supports_obsidian_keyboard_navigation() -> None:
    renderer = (
        DASHBOARD / "js" / "views" / "semantic-graph-render.js"
    ).read_text(encoding="utf-8")
    viewport = (
        DASHBOARD / "js" / "views" / "knowledge-graph-viewport.js"
    ).read_text(encoding="utf-8")

    assert 'tabindex: "0"' in renderer
    assert 'event.key === "+" || event.key === "="' in viewport
    assert 'event.key === "-" || event.key === "_"' in viewport
    for key in ("ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown"):
        assert f'"{key}"' in viewport


def test_graph_viewport_refits_local_graph_after_container_resize() -> None:
    viewport = (
        DASHBOARD / "js" / "views" / "knowledge-graph-viewport.js"
    ).read_text(encoding="utf-8")

    assert "ResizeObserver" in viewport
    assert "lastFitNodeIds" in viewport
    assert "scheduleFit" in viewport


def test_graph_ui_exposes_progressive_disclosure_and_direct_local_action(
) -> None:
    html = (DASHBOARD / "index.html").read_text(encoding="utf-8")

    for identifier in (
        "graph-smart-density", "graph-group-clusters",
        "graph-relation-filter", "graph-active-filters",
        "graph-focus-local", "graph-node-directory",
    ):
        assert f'id="{identifier}"' in html


def test_graph_nodes_use_roving_tabindex_and_spatial_navigation() -> None:
    renderer = (
        DASHBOARD / "js" / "views" / "semantic-graph-render.js"
    ).read_text(encoding="utf-8")
    accessibility = (
        DASHBOARD / "js" / "views" / "knowledge-graph-accessibility.js"
    ).read_text(encoding="utf-8")

    assert 'tabindex: index === 0 ? "0" : "-1"' in renderer
    assert "bindRovingGraphNavigation" in renderer
    assert "nearestDirectionalNode" in accessibility
    assert "ArrowLeft" in accessibility


def test_smart_density_hides_low_value_leaf_entities() -> None:
    node = shutil.which("node")
    if node is None:
        pytest.skip("Node.js is unavailable")
    module = DASHBOARD / "js" / "views" / "knowledge-graph-explorer.js"
    graph = {
        "nodes": [
            {"id": "doc", "type": "document", "kind": "document"},
            {"id": "useful", "type": "entity", "kind": "concept", "mentions": 4},
            {"id": "noise", "type": "entity", "kind": "term", "mentions": 1},
        ],
        "edges": [
            {"id": "du", "source": "doc", "target": "useful", "type": "mentions"},
            {"id": "dn", "source": "doc", "target": "noise", "type": "mentions"},
        ],
    }
    script = (
        f'import {{ visibleGraphElements }} from {json.dumps(module.as_uri())};'
        f"const graph={json.dumps(graph)};"
        "const visible=visibleGraphElements(graph,{local:false,depth:1,"
        "documents:true,entities:true,smart:true,relation:'all'},'');"
        "if(visible.nodeIds.has('noise'))process.exit(2);"
        "if(!visible.nodeIds.has('useful')||!visible.nodeIds.has('doc'))process.exit(3);"
    )

    completed = subprocess.run(
        [node, "--input-type=module", "--eval", script],
        check=False,
        capture_output=True,
        text=True,
        timeout=15,
    )

    assert completed.returncode == 0, completed.stderr
