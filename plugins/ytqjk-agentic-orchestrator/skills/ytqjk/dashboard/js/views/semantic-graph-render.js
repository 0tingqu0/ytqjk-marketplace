import { bindKnowledgeGraphMotion } from "./knowledge-graph-motion.js";
import { bindGraphNodeDrag } from "./knowledge-graph-drag.js";
import {
  nearestGraphElement,
  syncGraphNodeHitTargets,
  watchGraphNodeHitTargets,
} from "./knowledge-graph-hit-targets.js";
import { layoutKnowledgeGraph } from "./knowledge-graph-layout.js";
import { bindGraphViewport } from "./knowledge-graph-viewport.js";
import { bindRovingGraphNavigation } from "./knowledge-graph-accessibility.js";

const SVG_NS = "http://www.w3.org/2000/svg";

function svgNode(name, attributes = {}, className = "") {
  const node = document.createElementNS(SVG_NS, name);
  Object.entries(attributes).forEach(([key, value]) => {
    node.setAttribute(key, String(value));
  });
  if (className) node.setAttribute("class", className);
  return node;
}

function shortened(value, length = 18) {
  return value.length > length ? `${value.slice(0, length - 1)}…` : value;
}

function edgeCoordinates(source, target) {
  return {
    x1: source.x,
    y1: source.y,
    x2: target.x,
    y2: target.y,
  };
}

function edgeNode(edge, source, target, onSelect) {
  const group = svgNode("g", {
    "aria-hidden": "true",
    focusable: "false",
  }, "semantic-edge-link");
  group.dataset.edge = edge.id;
  group.dataset.source = edge.source;
  group.dataset.target = edge.target;
  const title = svgNode("title");
  title.textContent = `${edge.label} · ${Math.round(edge.confidence * 100)}%`;
  const hit = svgNode("line", edgeCoordinates(source, target), "semantic-edge-hit");
  const line = svgNode(
    "line",
    edgeCoordinates(source, target),
    `graph-edge semantic-edge semantic-edge--${edge.type}`,
  );
  group.append(title, hit, line);
  group.onclick = (event) => {
    event.stopPropagation();
    onSelect(edge);
  };
  return group;
}

function edgeLabel(edge, source, target) {
  const label = svgNode("text", {
    x: (source.x + target.x) / 2,
    y: (source.y + target.y) / 2 - 5,
    "text-anchor": "middle",
  }, "semantic-edge-label");
  label.dataset.edge = edge.id;
  label.textContent = edge.label;
  return label;
}

function nodeRadius(node, degree) {
  const importance = Math.max(node.mentions || 1, degree || 0);
  if (node.type === "document") {
    return Math.min(12, 6.4 + Math.log2(importance + 1) * 1.15);
  }
  return Math.min(9.5, 3.8 + Math.log2(importance + 1));
}

function displayLabel(node) {
  return node.display_label || node.label;
}

function graphNode(node, position, cluster, degree, index) {
  const group = svgNode(
    "g",
    {
      transform: `translate(${position.x.toFixed(2)} ${position.y.toFixed(2)})`,
      tabindex: index === 0 ? "0" : "-1",
      role: "button",
      "aria-label": `${node.type === "document" ? "文档" : "实体"} ${displayLabel(node)}`,
    },
    `graph-node-link semantic-node semantic-node--${node.type}`,
  );
  group.dataset.node = node.id;
  group.dataset.cluster = cluster;
  group.dataset.x = position.x.toFixed(2);
  group.dataset.y = position.y.toFixed(2);
  const title = svgNode("title");
  title.textContent = node.path ? `${displayLabel(node)} · ${node.path}` : displayLabel(node);
  const radius = nodeRadius(node, degree);
  const hit = svgNode(
    "circle", { r: 22 }, "graph-node-hit semantic-node-hit",
  );
  const focusRing = svgNode("circle", {
    r: radius + 4,
    "vector-effect": "non-scaling-stroke",
  }, "semantic-node-focus-ring");
  const dot = svgNode("circle", {
    r: radius,
    "data-base-radius": radius,
  }, "graph-node-dot");
  const label = svgNode("text", {
    x: 11,
    y: 4,
    "text-anchor": "start",
  }, [
    "graph-node-label",
    degree >= 4 ? "is-secondary" : "",
    degree >= 7 ? "is-prominent" : "",
  ].filter(Boolean).join(" "));
  label.textContent = shortened(displayLabel(node));
  group.append(title, hit, focusRing, dot, label);
  return group;
}

export function bindSemanticNodeSelection(svg, graph, onSelect) {
  const nodes = new Map(graph.nodes.map((node) => [node.id, node]));
  svg.onclick = (event) => {
    const selector = ".semantic-node:not(.is-filtered-out)";
    const element = event.target?.closest?.(selector)
      || nearestGraphElement(svg, event, selector);
    const node = nodes.get(element?.dataset.node);
    if (node) onSelect(node);
  };
  return (nodeId) => {
    const node = nodes.get(nodeId);
    if (node) onSelect(node);
  };
}

function clusterNode(cluster) {
  const group = svgNode("g", {}, "semantic-cluster");
  group.dataset.cluster = cluster.id;
  group.dataset.label = cluster.label;
  const halo = svgNode("circle", {
    cx: cluster.x,
    cy: cluster.y,
    r: cluster.radius,
  }, "semantic-cluster-halo");
  const label = svgNode("text", {
    x: cluster.x - cluster.radius + 14,
    y: cluster.y - cluster.radius + 22,
  }, "semantic-cluster-label");
  label.textContent = `${shortened(cluster.label, 22)} · ${cluster.count}`;
  group.append(halo, label);
  return group;
}

function statsNode(graph) {
  const stats = document.createElement("div");
  stats.className = "graph-stats";
  const documentCount = graph.nodes.filter((node) => (
    node.type === "document"
  )).length;
  [
    ["documents", "文档", documentCount],
    ["entities", "实体", graph.nodes.length - documentCount],
    ["relations", "关系", graph.edges.length],
  ].forEach(([key, label, value]) => {
    const item = document.createElement("span");
    item.dataset.stat = key;
    item.dataset.total = String(value);
    const count = document.createElement("b");
    count.textContent = `${value}/${value}`;
    item.append(count, document.createTextNode(label));
    stats.append(item);
  });
  return stats;
}

export function renderSemanticGraph(
  target, graph, onSelect, onEdgeSelect, onZoom,
) {
  const layout = layoutKnowledgeGraph(graph);
  const svg = svgNode("svg", {
    viewBox: `0 0 ${layout.width} ${layout.height}`,
    tabindex: "0",
    role: "group",
    "aria-label": "知识文档、实体以及语义关系图",
  }, "knowledge-graph semantic-knowledge-graph");
  const pointerSurface = svgNode("rect", {
    width: layout.width,
    height: layout.height,
    "aria-hidden": "true",
  }, "graph-pointer-surface");
  const clusterLayer = svgNode("g", {}, "semantic-clusters");
  const edgeLayer = svgNode("g", {}, "graph-edges semantic-edges");
  const labelLayer = svgNode("g", {}, "semantic-edge-labels");
  const nodeLayer = svgNode("g", {}, "graph-nodes semantic-nodes");
  const degree = new Map(graph.nodes.map((node) => [node.id, 0]));
  graph.edges.forEach((edge) => {
    degree.set(edge.source, (degree.get(edge.source) || 0) + 1);
    degree.set(edge.target, (degree.get(edge.target) || 0) + 1);
    const source = layout.positions.get(edge.source);
    const targetPosition = layout.positions.get(edge.target);
    if (!source || !targetPosition) return;
    edgeLayer.append(edgeNode(edge, source, targetPosition, onEdgeSelect));
    if (edge.type !== "mentions") {
      labelLayer.append(edgeLabel(edge, source, targetPosition));
    }
  });
  layout.clusters.forEach((cluster) => clusterLayer.append(clusterNode(cluster)));
  graph.nodes.forEach((node, index) => {
    const position = layout.positions.get(node.id);
    if (position) nodeLayer.append(graphNode(
      node, position, layout.nodeClusters.get(node.id),
      degree.get(node.id) || 0, index,
    ));
  });
  svg.append(pointerSurface, clusterLayer, edgeLayer, labelLayer, nodeLayer);
  target.graphViewport?.destroy?.();
  target.replaceChildren(svg, statsNode(graph));
  const motion = bindKnowledgeGraphMotion(target);
  const activateNode = bindSemanticNodeSelection(svg, graph, onSelect);
  bindRovingGraphNavigation(svg, activateNode);
  const drag = bindGraphNodeDrag(svg, target, graph, layout);
  const viewport = bindGraphViewport(svg, target, (state) => {
    syncGraphNodeHitTargets(svg, ".semantic-node-hit");
    onZoom?.(state);
  });
  const stopHitObserver = watchGraphNodeHitTargets(svg, ".semantic-node-hit");
  const destroyViewport = viewport.destroy.bind(viewport);
  viewport.destroy = () => {
    motion.destroy();
    drag.destroy();
    stopHitObserver();
    destroyViewport();
  };
  target.graphViewport = viewport;
  return viewport;
}

export function applyGraphHighlight(
  target, selectedId, selectedEdgeId = "", pathNodeIds = new Set(),
  pathEdgeIds = new Set(),
) {
  const neighbors = new Set();
  target.querySelectorAll(".semantic-edge-link").forEach((edge) => {
    const connected = edge.dataset.source === selectedId
      || edge.dataset.target === selectedId;
    edge.classList.toggle("is-neighbor", connected);
    edge.classList.toggle("is-selected", edge.dataset.edge === selectedEdgeId);
    if (connected) {
      neighbors.add(edge.dataset.source);
      neighbors.add(edge.dataset.target);
    }
  });
  target.querySelectorAll("[data-node]").forEach((node) => {
    node.classList.toggle("is-selected", node.dataset.node === selectedId);
    node.classList.toggle("is-neighbor", neighbors.has(node.dataset.node));
    node.classList.toggle("is-path", pathNodeIds.has(node.dataset.node));
  });
  target.querySelectorAll("[data-edge]").forEach((edge) => {
    edge.classList.toggle("is-path", pathEdgeIds.has(edge.dataset.edge));
  });
  target.classList.toggle("has-selection", Boolean(selectedId));
  target.classList.toggle("has-path", pathNodeIds.size > 0);
}
