import { bindKnowledgeGraphMotion } from "./knowledge-graph-motion.js";
import { layoutKnowledgeGraph } from "./knowledge-graph-layout.js";

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

function edgeNode(edge, source, target) {
  const line = svgNode("line", {
    x1: source.x,
    y1: source.y,
    x2: target.x,
    y2: target.y,
  }, `graph-edge semantic-edge semantic-edge--${edge.type}`);
  line.dataset.edge = edge.id;
  line.dataset.source = edge.source;
  line.dataset.target = edge.target;
  return line;
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

function nodeRadius(node) {
  if (node.type === "document") return 7.5;
  return Math.min(8, 3.8 + Math.log2((node.mentions || 1) + 1));
}

function graphNode(node, position, degree, onSelect) {
  const group = svgNode(
    "g",
    {
      transform: `translate(${position.x.toFixed(2)} ${position.y.toFixed(2)})`,
      tabindex: "0",
      role: "button",
      "aria-label": `${node.type === "document" ? "文档" : "实体"} ${node.label}`,
    },
    `graph-node-link semantic-node semantic-node--${node.type}`,
  );
  group.dataset.node = node.id;
  const title = svgNode("title");
  title.textContent = node.path ? `${node.label} · ${node.path}` : node.label;
  const hit = svgNode("circle", { r: 16 }, "graph-node-hit");
  const dot = svgNode("circle", { r: nodeRadius(node) }, "graph-node-dot");
  const label = svgNode("text", {
    x: 11,
    y: 4,
    "text-anchor": "start",
  }, `graph-node-label${degree >= 5 ? " is-prominent" : ""}`);
  label.textContent = shortened(node.label);
  group.append(title, hit, dot, label);
  group.onclick = () => onSelect(node);
  group.onkeydown = (event) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onSelect(node);
    }
  };
  return group;
}

function statsNode(graph) {
  const stats = document.createElement("div");
  stats.className = "graph-stats";
  [
    ["文档", graph.stats.documents],
    ["实体", graph.stats.entities],
    ["关系", graph.stats.relations],
  ].forEach(([label, value]) => {
    const item = document.createElement("span");
    const count = document.createElement("b");
    count.textContent = String(value);
    item.append(count, document.createTextNode(label));
    stats.append(item);
  });
  return stats;
}

export function renderSemanticGraph(target, graph, onSelect) {
  const layout = layoutKnowledgeGraph(graph);
  const svg = svgNode("svg", {
    viewBox: `0 0 ${layout.width} ${layout.height}`,
    role: "group",
    "aria-label": "知识文档、实体以及语义关系图",
  }, "knowledge-graph semantic-knowledge-graph");
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
    edgeLayer.append(edgeNode(edge, source, targetPosition));
    if (edge.type !== "mentions") {
      labelLayer.append(edgeLabel(edge, source, targetPosition));
    }
  });
  graph.nodes.forEach((node) => {
    const position = layout.positions.get(node.id);
    if (position) nodeLayer.append(graphNode(
      node, position, degree.get(node.id) || 0, onSelect,
    ));
  });
  svg.append(edgeLayer, labelLayer, nodeLayer);
  target.replaceChildren(svg, statsNode(graph));
  bindKnowledgeGraphMotion(target);
}

export function applyGraphHighlight(
  target, selectedId, pathNodeIds = new Set(), pathEdgeIds = new Set(),
) {
  target.querySelectorAll("[data-node]").forEach((node) => {
    node.classList.toggle("is-selected", node.dataset.node === selectedId);
    node.classList.toggle("is-path", pathNodeIds.has(node.dataset.node));
  });
  target.querySelectorAll("[data-edge]").forEach((edge) => {
    edge.classList.toggle("is-path", pathEdgeIds.has(edge.dataset.edge));
  });
  target.classList.toggle("has-path", pathNodeIds.size > 0);
}
