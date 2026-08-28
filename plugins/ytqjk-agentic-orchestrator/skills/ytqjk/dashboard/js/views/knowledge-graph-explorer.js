import { byId, clear, text } from "../ui/dom.js";
import { syncRovingTabStop } from "./knowledge-graph-accessibility.js";

const STORAGE_KEY = "ytqjk.semanticGraph.settings.v2";
const DEFAULTS = Object.freeze({
  local: false,
  depth: 1,
  documents: true,
  entities: true,
  smart: true,
  grouped: true,
  relation: "all",
  labels: false,
  relations: false,
  nodeScale: 1,
  linkScale: 1,
});

function adjacency(graph) {
  const neighbors = new Map((graph.nodes || []).map((node) => [node.id, []]));
  (graph.edges || []).forEach((edge) => {
    if (!neighbors.has(edge.source) || !neighbors.has(edge.target)) return;
    neighbors.get(edge.source).push({ nodeId: edge.target, edgeId: edge.id });
    neighbors.get(edge.target).push({ nodeId: edge.source, edgeId: edge.id });
  });
  return neighbors;
}

export function graphNeighborhood(graph, selectedId, depth = 1) {
  const neighbors = adjacency(graph);
  const nodeIds = new Set();
  const edgeIds = new Set();
  if (!neighbors.has(selectedId)) return { nodeIds, edgeIds };
  nodeIds.add(selectedId);
  let frontier = new Set([selectedId]);
  for (let level = 0; level < depth; level += 1) {
    const next = new Set();
    frontier.forEach((nodeId) => {
      neighbors.get(nodeId).forEach((neighbor) => {
        edgeIds.add(neighbor.edgeId);
        if (!nodeIds.has(neighbor.nodeId)) next.add(neighbor.nodeId);
        nodeIds.add(neighbor.nodeId);
      });
    });
    frontier = next;
    if (!frontier.size) break;
  }
  return { nodeIds, edgeIds };
}

function relationMatches(edge, relation) {
  if (relation === "all") return true;
  if (relation === "explicit") {
    return edge.type !== "mentions" && edge.type !== "similar_to";
  }
  return edge.type === relation;
}

function importantNode(node, degree) {
  return node.type === "document"
    || Number(node.mentions || 0) >= 2
    || Number(node.document_count || 0) >= 2
    || degree > 1;
}

function importantEdge(edge) {
  const confidence = Number(edge.confidence);
  if (!Number.isFinite(confidence)) return true;
  if (edge.type === "mentions") return confidence >= 0.78 || edge.weight > 1;
  if (edge.type === "similar_to") return confidence >= 0.55;
  return true;
}

export function visibleGraphElements(graph, settings, selectedId) {
  const local = settings.local
    ? graphNeighborhood(graph, selectedId, settings.depth).nodeIds
    : new Set((graph.nodes || []).map((node) => node.id));
  const degree = new Map((graph.nodes || []).map((node) => [node.id, 0]));
  (graph.edges || []).forEach((edge) => {
    degree.set(edge.source, (degree.get(edge.source) || 0) + 1);
    degree.set(edge.target, (degree.get(edge.target) || 0) + 1);
  });
  const nodeIds = new Set((graph.nodes || []).filter((node) => (
    local.has(node.id)
    && (node.type === "document" ? settings.documents : settings.entities)
    && (
      !settings.smart
      || node.id === selectedId
      || importantNode(node, degree.get(node.id) || 0)
    )
  )).map((node) => node.id));
  const edgeIds = new Set((graph.edges || []).filter((edge) => (
    nodeIds.has(edge.source)
    && nodeIds.has(edge.target)
    && relationMatches(edge, settings.relation)
    && (!settings.smart || importantEdge(edge))
  )).map((edge) => edge.id));
  if (settings.smart) {
    const connected = new Set([selectedId]);
    graph.edges.forEach((edge) => {
      if (!edgeIds.has(edge.id)) return;
      connected.add(edge.source);
      connected.add(edge.target);
    });
    graph.nodes.forEach((node) => {
      if (node.type !== "document" && !connected.has(node.id)) {
        nodeIds.delete(node.id);
      }
    });
  }
  if (settings.relation !== "all") {
    const connected = new Set([selectedId]);
    graph.edges.forEach((edge) => {
      if (!edgeIds.has(edge.id)) return;
      connected.add(edge.source);
      connected.add(edge.target);
    });
    [...nodeIds].forEach((id) => {
      if (!connected.has(id)) nodeIds.delete(id);
    });
  }
  return { nodeIds, edgeIds };
}

function readSettings() {
  return {
    local: byId("graph-local-enabled").checked,
    depth: Number(byId("graph-local-depth").value),
    documents: byId("graph-show-documents").checked,
    entities: byId("graph-show-entities").checked,
    smart: byId("graph-smart-density").checked,
    grouped: byId("graph-group-clusters").checked,
    relation: byId("graph-relation-filter").value,
    labels: byId("graph-show-labels").checked,
    relations: byId("graph-show-relations").checked,
    nodeScale: Number(byId("graph-node-scale").value),
    linkScale: Number(byId("graph-link-scale").value),
  };
}

function writeSettings(settings) {
  byId("graph-local-enabled").checked = settings.local;
  byId("graph-local-depth").value = String(settings.depth);
  byId("graph-show-documents").checked = settings.documents;
  byId("graph-show-entities").checked = settings.entities;
  byId("graph-smart-density").checked = settings.smart;
  byId("graph-group-clusters").checked = settings.grouped;
  byId("graph-relation-filter").value = settings.relation;
  byId("graph-show-labels").checked = settings.labels;
  byId("graph-show-relations").checked = settings.relations;
  byId("graph-node-scale").value = String(settings.nodeScale);
  byId("graph-link-scale").value = String(settings.linkScale);
}

function savedSettings() {
  try {
    return { ...DEFAULTS, ...JSON.parse(localStorage.getItem(STORAGE_KEY) || "{}") };
  } catch (error) {
    console.debug("Ignoring invalid graph preferences", error);
    return { ...DEFAULTS };
  }
}

function persist(settings) {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      ...settings,
      local: false,
    }));
  } catch (error) {
    console.debug("Graph preferences are not persistent", error);
  }
}

function updateOutputs(settings) {
  byId("graph-local-depth-value").textContent = `${settings.depth} 跳`;
  byId("graph-node-scale-value").textContent = `${settings.nodeScale * 100}%`;
  byId("graph-link-scale-value").textContent = `${settings.linkScale * 100}%`;
  byId("graph-local-depth").setAttribute("aria-valuetext", `${settings.depth} 跳`);
  byId("graph-node-scale").setAttribute(
    "aria-valuetext", `${settings.nodeScale * 100}%`,
  );
  byId("graph-link-scale").setAttribute(
    "aria-valuetext", `${settings.linkScale * 100}%`,
  );
}

function applyVisibility(target, graph, settings, selectedId) {
  const visible = visibleGraphElements(graph, settings, selectedId);
  target.querySelectorAll("[data-node]").forEach((node) => {
    node.classList.toggle("is-filtered-out", !visible.nodeIds.has(node.dataset.node));
  });
  target.querySelectorAll("[data-edge]").forEach((edge) => {
    edge.classList.toggle("is-filtered-out", !visible.edgeIds.has(edge.dataset.edge));
  });
  return visible;
}

function applyScale(target, settings) {
  target.querySelectorAll(".graph-node-dot[data-base-radius]").forEach((dot) => {
    dot.setAttribute(
      "r", String(Number(dot.dataset.baseRadius) * settings.nodeScale),
    );
  });
  target.style.setProperty("--semantic-link-scale", settings.linkScale);
}

function updateActiveFilters(settings, visible, graph) {
  const labels = [];
  if (settings.smart) labels.push("智能降噪");
  if (settings.local) labels.push(`${settings.depth} 跳局部图`);
  if (!settings.documents) labels.push("隐藏文档");
  if (!settings.entities) labels.push("隐藏实体");
  if (settings.relation !== "all") {
    labels.push(byId("graph-relation-filter").selectedOptions[0].textContent);
  }
  labels.push(`${visible.nodeIds.size}/${graph.nodes.length} 节点`);
  clear(byId("graph-active-filters"), labels.map((label) => (
    text("span", label, "graph-filter-chip")
  )));
}

function setPanel(open) {
  byId("graph-settings-panel").hidden = !open;
  byId("graph-settings-toggle").setAttribute("aria-expanded", String(open));
}

export function bindGraphExplorer(context) {
  const inputIds = [
    "graph-local-enabled", "graph-local-depth", "graph-show-documents",
    "graph-show-entities", "graph-smart-density", "graph-group-clusters",
    "graph-relation-filter", "graph-show-labels", "graph-show-relations",
    "graph-node-scale", "graph-link-scale",
  ];

  function sync() {
    const { target, graph, selectedId, viewport } = context();
    if (!target || !graph) return;
    const settings = readSettings();
    if (!selectedId && settings.local) {
      settings.local = false;
      byId("graph-local-enabled").checked = false;
    }
    const visible = applyVisibility(target, graph, settings, selectedId);
    applyScale(target, settings);
    if (settings.local) viewport?.fit(visible.nodeIds);
    else viewport?.clearFit?.();
    target.classList.toggle("show-all-node-labels", settings.labels);
    target.classList.toggle("show-relation-labels", settings.relations);
    target.classList.toggle("show-graph-clusters", settings.grouped);
    target.classList.toggle("is-local-graph", settings.local);
    target.classList.toggle("has-graph-filter", visible.nodeIds.size < graph.nodes.length);
    byId("graph-local-enabled").disabled = !selectedId;
    byId("graph-local-depth").disabled = !selectedId || !settings.local;
    byId("graph-local-status").textContent = selectedId
      ? (settings.local
        ? `当前显示 ${visible.nodeIds.size} 个节点、${visible.edgeIds.size} 条连接`
        : "可直接在节点详情中打开一跳局部图")
      : "选择节点后可开启局部图谱";
    updateOutputs(settings);
    updateActiveFilters(settings, visible, graph);
    syncRovingTabStop(target.querySelector("svg"), selectedId);
    persist(settings);
    byId("graph-node-directory").dispatchEvent(
      new CustomEvent("graphvisibilitychange"),
    );
  }

  inputIds.map(byId).forEach((input) => input.addEventListener("input", sync));
  byId("graph-settings-toggle").onclick = () => setPanel(
    byId("graph-settings-panel").hidden,
  );
  byId("graph-settings-close").onclick = () => setPanel(false);
  byId("graph-settings-reset").onclick = () => {
    writeSettings(DEFAULTS);
    sync();
  };
  writeSettings(savedSettings());
  setPanel(false);
  return {
    sync,
    focusLocal() {
      byId("graph-local-enabled").checked = true;
      byId("graph-local-depth").value = "1";
      sync();
      setPanel(false);
    },
  };
}
