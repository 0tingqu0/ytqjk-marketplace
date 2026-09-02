from __future__ import annotations

import json
import shutil
import subprocess
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).resolve().parents[2]
DASHBOARD = SKILL_ROOT / "dashboard"


def test_semantic_graph_binds_and_cleans_up_node_dragging() -> None:
    renderer = (
        DASHBOARD / "js" / "views" / "semantic-graph-render.js"
    ).read_text(encoding="utf-8")
    drag = (
        DASHBOARD / "js" / "views" / "knowledge-graph-drag.js"
    ).read_text(encoding="utf-8")
    html = (DASHBOARD / "index.html").read_text(encoding="utf-8")

    assert "bindGraphNodeDrag" in renderer
    assert "drag.destroy()" in renderer
    assert 'addEventListener("pointerdown"' in drag
    assert "setPointerCapture" in drag
    assert "拖动节点可弹性重排" in html


def test_graph_drag_styles_only_animate_while_interacting() -> None:
    css = (DASHBOARD / "knowledge-graph-workbench.css").read_text(
        encoding="utf-8"
    )

    assert ".semantic-node.is-dragging" in css
    assert ".knowledge-topology.is-graph-settling" in css
    assert "prefers-reduced-motion: reduce" in css
    assert "animation: none" in css


def test_drag_physics_pulls_neighbors_and_stays_finite() -> None:
    node = shutil.which("node")
    if node is None:
        pytest.skip("Node.js is unavailable")
    module = DASHBOARD / "js" / "views" / "knowledge-graph-drag.js"
    script = (
        f'import {{ stepGraphPhysics }} from {json.dumps(module.as_uri())};'
        "const points=new Map(["
        "['a',{id:'a',x:180,y:100,vx:0,vy:0,cluster:'c'}],"
        "['b',{id:'b',x:80,y:100,vx:0,vy:0,cluster:'c'}],"
        "['c',{id:'c',x:80,y:160,vx:0,vy:0,cluster:'c'}]]);"
        "const edges=[{source:'a',target:'b',length:50}];"
        "const movable=new Set(['a','b']);"
        "const pinnedX=points.get('a').x;"
        "stepGraphPhysics(points,edges,movable,'a',1);"
        "if(points.get('a').x!==pinnedX)process.exit(2);"
        "if(points.get('b').x<=80)process.exit(3);"
        "let heat=1;"
        "for(let index=0;index<180;index+=1){"
        "stepGraphPhysics(points,edges,movable,'',heat);heat*=0.93;}"
        "const values=[...points.values()].flatMap((point)=>"
        "[point.x,point.y,point.vx,point.vy]);"
        "if(!values.every(Number.isFinite))process.exit(4);"
        "if(points.get('c').x!==80||points.get('c').y!==160)process.exit(5);"
    )

    completed = subprocess.run(
        [node, "--input-type=module", "--eval", script],
        check=False,
        capture_output=True,
        text=True,
        timeout=15,
    )

    assert completed.returncode == 0, completed.stderr
