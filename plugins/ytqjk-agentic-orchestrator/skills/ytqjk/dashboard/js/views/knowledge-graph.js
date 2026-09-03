import { bindKnowledgeGraphMotion } from "./knowledge-graph-motion.js";
import {
  nearestGraphElement,
  watchGraphNodeHitTargets,
} from "./knowledge-graph-hit-targets.js";

const SVG_NS = "http://www.w3.org/2000/svg";
const GRAPH = { width: 820, height: 600, centerX: 410, centerY: 300 };

function svgNode(name, attributes = {}, className = "") {
  const node = document.createElementNS(SVG_NS, name);
  Object.entries(attributes).forEach(([key, value]) => {
    node.setAttribute(key, String(value));
  });
  if (className) node.setAttribute("class", className);
  return node;
}

function graphPoint(centerX, centerY, radiusX, radiusY, angle) {
  return {
    x: centerX + Math.cos(angle) * radiusX,
    y: centerY + Math.sin(angle) * radiusY,
  };
}

function distributeProjects(projects) {
  if (projects.length <= 12) {
    return projects.map((project, index) => ({
      project,
      ...graphPoint(
        GRAPH.centerX,
        GRAPH.centerY,
        270,
        218,
        -Math.PI / 2 + (Math.PI * 2 * index) / projects.length,
      ),
    }));
  }
  const innerCount = 8;
  return projects.map((project, index) => {
    const outer = index >= innerCount;
    const ringIndex = outer ? index - innerCount : index;
    const ringCount = outer ? projects.length - innerCount : innerCount;
    const angle = -Math.PI / 2
      + (Math.PI * 2 * ringIndex) / ringCount
      + (outer ? Math.PI / ringCount : 0);
    return {
      project,
      ...graphPoint(
        GRAPH.centerX,
        GRAPH.centerY,
        outer ? 325 : 195,
        outer ? 255 : 165,
        angle,
      ),
    };
  });
}

function edge(from, to, className = "", order = 0) {
  const node = svgNode("line", {
    x1: from.x,
    y1: from.y,
    x2: to.x,
    y2: to.y,
  }, `graph-edge ${className}`.trim());
  node.dataset.source = from.id;
  node.dataset.target = to.id;
  node.style.setProperty("--graph-order", String(order));
  return node;
}

function labelFor(path) {
  const name = String(path || "知识文档").split(/[\\/]/).pop();
  return name.length > 18 ? `${name.slice(0, 17)}…` : name;
}

function graphLink(position, options) {
  const link = svgNode("a", { href: `#${options.route}` }, "graph-node-link");
  link.setAttribute("aria-label", options.ariaLabel);
  link.dataset.node = options.nodeId;
  link.dataset.graphX = String(position.x);
  link.dataset.graphY = String(position.y);
  link.onkeydown = (event) => {
    if (!["Enter", " "].includes(event.key)) return;
    event.preventDefault();
    window.location.hash = link.getAttribute("href");
  };
  const group = svgNode(
    "g",
    { transform: `translate(${position.x} ${position.y})` },
    `graph-node graph-node--${options.type} ${options.state || ""}`.trim(),
  );
  const title = svgNode("title");
  title.textContent = options.title;
  const hit = svgNode(
    "circle",
    { r: options.hitRadius || 18 },
    "graph-node-hit",
  );
  const focusRing = svgNode(
    "circle",
    { r: options.hitRadius || 18 },
    "graph-node-focus-ring",
  );
  const dot = svgNode("circle", { r: options.radius || 7 }, "graph-node-dot");
  group.append(title, hit, focusRing, dot);
  if (options.label) {
    const label = svgNode("text", {
      x: options.labelX ?? (position.x >= GRAPH.centerX ? 13 : -13),
      y: options.labelY ?? 4,
      "text-anchor": options.textAnchor
        || (position.x >= GRAPH.centerX ? "start" : "end"),
    }, "graph-node-label");
    label.textContent = options.label;
    group.append(label);
  }
  link.append(group);
  return link;
}

function bindNearestNodePointer(svg) {
  svg.onclick = (event) => {
    if (event.detail === 0) return;
    const nearest = nearestGraphElement(svg, event, ".graph-node-link");
    if (!nearest) return;
    event.preventDefault();
    nearest.focus();
    window.location.hash = nearest.getAttribute("href");
  };
}

function sessionPosition(parent, index, total) {
  const ring = Math.floor(index / 5);
  const count = Math.min(5, total - ring * 5);
  const slot = index % 5;
  const angle = (Math.PI * 2 * slot) / Math.max(count, 1) + ring * 0.4;
  return graphPoint(parent.x, parent.y, 27 + ring * 13, 20 + ring * 10, angle);
}

function appendRoot(svg, edges, nodes, counts) {
  const root = { id: "root", x: GRAPH.centerX, y: GRAPH.centerY };
  const halo = svgNode("circle", {
    cx: root.x,
    cy: root.y,
    r: 34,
  }, "graph-root-halo");
  nodes.append(halo, graphLink(root, {
    route: "libraries",
    type: "root",
    nodeId: root.id,
    radius: 18,
    hitRadius: 34,
    title: `本地总库 · ${counts.approved} 条已批准经验`,
    ariaLabel: `本地总库，${counts.approved} 条已批准经验，进入知识库树`,
    label: "本地总库",
    labelX: 0,
    labelY: 51,
    textAnchor: "middle",
  }));
  svg.append(edges, nodes);
  return root;
}

function appendProjects(projects, sessions, root, edges, nodes) {
  const points = distributeProjects(projects);
  const pointByProject = new Map(points.map((item) => [item.project.id, item]));
  points.forEach((item, index) => {
    item.id = `project:${item.project.id}`;
    edges.append(edge(root, item, "graph-edge--project", index));
    nodes.append(graphLink(item, {
      route: "libraries",
      type: "project",
      nodeId: item.id,
      state: item.project.tracking === "INDEXED" ? "is-indexed" : "",
      radius: item.project.tracking === "INDEXED" ? 8 : 6,
      title: `${item.project.name} · ${item.project.tracking}`,
      ariaLabel: `项目子库 ${item.project.name}，进入知识库树`,
      label: item.project.name,
    }));
  });
  const grouped = new Map();
  sessions.forEach((session) => {
    if (!grouped.has(session.project)) grouped.set(session.project, []);
    grouped.get(session.project).push(session);
  });
  grouped.forEach((items, projectId) => {
    const parent = pointByProject.get(projectId) || root;
    items.slice(0, 10).forEach((session, index) => {
      const point = {
        id: `session:${session.key}`,
        ...sessionPosition(parent, index, items.length),
      };
      edges.append(edge(parent, point, "graph-edge--session", index));
      nodes.append(graphLink(point, {
        route: "sessions",
        type: "session",
        nodeId: point.id,
        state: session.archived_at ? "is-archived" : "",
        radius: session.archived_at ? 2.4 : 3.2,
        hitRadius: 10,
        title: `会话 ${session.key}`,
        ariaLabel: `会话锚点 ${session.key}，进入会话锚点`,
      }));
    });
  });
}

function appendDocuments(documents, root, edges, nodes) {
  documents.slice(0, 12).forEach((documentItem, index, visible) => {
    const point = {
      id: `document:${documentItem.path}`,
      ...graphPoint(
        root.x,
        root.y,
        82,
        68,
        -Math.PI / 2 + (Math.PI * 2 * index) / visible.length,
      ),
    };
    edges.append(edge(root, point, "graph-edge--document", index));
    nodes.append(graphLink(point, {
      route: "documents",
      type: "document",
      nodeId: point.id,
      state: `is-${documentItem.state}`,
      radius: 5,
      hitRadius: 13,
      title: documentItem.path,
      ariaLabel: `知识文档 ${documentItem.path}，进入知识文档`,
      label: labelFor(documentItem.path),
    }));
  });
}

function graphStats(snapshot) {
  const stats = document.createElement("div");
  stats.className = "graph-stats";
  const items = [
    ["项目", snapshot.projects.length],
    ["文档", snapshot.documents.length],
    ["会话", snapshot.sessions.length],
  ];
  items.forEach(([label, value]) => {
    const item = document.createElement("span");
    const count = document.createElement("b");
    count.textContent = String(value);
    item.append(count, document.createTextNode(label));
    stats.append(item);
  });
  return stats;
}

export function renderLibraryTopology(target, snapshot) {
  target.graphViewport?.destroy?.();
  target.graphViewport = null;
  const svg = svgNode("svg", {
    viewBox: `0 0 ${GRAPH.width} ${GRAPH.height}`,
    role: "group",
    "aria-label": "本地总库、项目子库、知识文档与会话锚点关系图",
  }, "knowledge-graph");
  const edges = svgNode("g", {}, "graph-edges");
  const nodes = svgNode("g", {}, "graph-nodes");
  svg.append(svgNode("rect", {
    width: GRAPH.width,
    height: GRAPH.height,
    "aria-hidden": "true",
  }, "graph-pointer-surface"));
  const root = appendRoot(svg, edges, nodes, snapshot.counts);
  appendProjects(snapshot.projects, snapshot.sessions, root, edges, nodes);
  appendDocuments(snapshot.documents, root, edges, nodes);
  target.replaceChildren(svg, graphStats(snapshot));
  const motion = bindKnowledgeGraphMotion(target);
  bindNearestNodePointer(svg);
  const stopHitObserver = watchGraphNodeHitTargets(svg);
  target.graphViewport = {
    destroy() {
      motion.destroy();
      stopHitObserver();
    },
  };
}

export const renderKnowledgeGraph = renderLibraryTopology;
