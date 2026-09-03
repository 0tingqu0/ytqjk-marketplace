import { byId, clear, text } from "../ui/dom.js";
import { syncRovingTabStop } from "./knowledge-graph-accessibility.js";
import { syncGraphClusters } from "./knowledge-graph-clusters.js";
import {
  graphDensityLimits,
  graphNeighborhood,
  visibleGraphElements,
} from "./knowledge-graph-density.js";

export { graphNeighborhood, visibleGraphElements };

const STORAGE_KEY = "ytqjk.semanticGraph.settings.v2";
const SETTINGS_MODAL_QUERY = "(max-width: 900px)";
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
  const visible = visibleGraphElements(
    graph, settings, selectedId, graphDensityLimits(),
  );
  target.querySelectorAll("[data-node]").forEach((node) => {
    node.classList.toggle("is-filtered-out", !visible.nodeIds.has(node.dataset.node));
  });
  target.querySelectorAll("[data-edge]").forEach((edge) => {
    edge.classList.toggle("is-filtered-out", !visible.edgeIds.has(edge.dataset.edge));
  });
  const values = {
    documents: graph.nodes.filter((node) => (
      node.type === "document" && visible.nodeIds.has(node.id)
    )).length,
    entities: graph.nodes.filter((node) => (
      node.type !== "document" && visible.nodeIds.has(node.id)
    )).length,
    relations: visible.edgeIds.size,
  };
  target.querySelectorAll(".graph-stats [data-stat]").forEach((item) => {
    const total = item.dataset.total;
    item.querySelector("b").textContent = `${values[item.dataset.stat]}/${total}`;
    item.title = `当前可见 ${values[item.dataset.stat]}，总计 ${total}`;
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
  labels.push(`${visible.edgeIds.size}/${graph.edges.length} 关系`);
  clear(byId("graph-active-filters"), labels.map((label) => (
    text("span", label, "graph-filter-chip")
  )));
}

function panelControls(panel) {
  return [...panel.querySelectorAll(
    "button:not([disabled]), input:not([disabled]), select:not([disabled]), "
      + "summary, [tabindex]:not([tabindex='-1'])",
  )].filter((control) => (
    !control.closest("[hidden]")
    && (!control.closest("details:not([open])") || control.matches("summary"))
  ));
}

let panelHome;
const backgroundInertState = new Map();

function isSettingsModal() {
  return Boolean(window.matchMedia?.(SETTINGS_MODAL_QUERY).matches);
}

function setBackgroundInert(panel, inert) {
  if (inert) {
    [...document.body.children].forEach((node) => {
      if (node === panel || node.tagName === "SCRIPT") return;
      if (!backgroundInertState.has(node)) {
        backgroundInertState.set(node, node.inert);
      }
      node.inert = true;
    });
    return;
  }
  backgroundInertState.forEach((wasInert, node) => {
    if (node.isConnected) node.inert = wasInert;
  });
  backgroundInertState.clear();
}

function restorePanelHome(panel) {
  if (!panelHome || panel.parentNode === panelHome.parent) return;
  if (panelHome.next?.parentNode === panelHome.parent) {
    panelHome.parent.insertBefore(panel, panelHome.next);
  } else {
    panelHome.parent.append(panel);
  }
}

function setPanel(open, restoreFocus = false) {
  const panel = byId("graph-settings-panel");
  const toggle = byId("graph-settings-toggle");
  panelHome ||= { parent: panel.parentNode, next: panel.nextSibling };
  const modal = open && isSettingsModal();
  if (modal && panel.parentNode !== document.body) document.body.append(panel);
  panel.hidden = !open;
  if (modal) panel.setAttribute("aria-modal", "true");
  else panel.removeAttribute("aria-modal");
  setBackgroundInert(panel, modal);
  toggle.setAttribute("aria-expanded", String(open));
  document.body.classList.toggle("graph-settings-open", open);
  if (open) {
    requestAnimationFrame(() => {
      if (!panel.hidden) byId("graph-settings-close").focus();
    });
  } else {
    restorePanelHome(panel);
    if (restoreFocus) toggle.focus();
  }
}

export function bindGraphExplorer(context) {
  byId("graph-settings-panel").setAttribute("role", "dialog");
  let lastViewport = null;
  let lastFitKey = "";
  let lastWasFitted = false;
  const inputIds = [
    "graph-local-enabled", "graph-local-depth", "graph-show-documents",
    "graph-show-entities", "graph-smart-density", "graph-group-clusters",
    "graph-relation-filter", "graph-show-labels", "graph-show-relations",
    "graph-node-scale", "graph-link-scale",
  ];

  function sync(event) {
    const { target, graph, selectedId, viewport } = context();
    if (!target || !graph) return;
    const settings = readSettings();
    if (event?.type === "input" && event.target?.matches?.("[id$='-scale']")) {
      applyScale(target, settings);
      updateOutputs(settings);
      return;
    }
    if (!selectedId && settings.local) {
      settings.local = false;
      byId("graph-local-enabled").checked = false;
    }
    const visible = applyVisibility(target, graph, settings, selectedId);
    syncGraphClusters(target);
    applyScale(target, settings);
    const shouldFit = settings.local || visible.nodeIds.size < graph.nodes.length;
    const fitKey = shouldFit ? [...visible.nodeIds].sort().join("\u001f") : "";
    const fitChanged = viewport !== lastViewport
      || shouldFit !== lastWasFitted
      || (shouldFit && fitKey !== lastFitKey);
    if (fitChanged) {
      if (shouldFit) viewport?.fit(visible.nodeIds);
      else viewport?.clearFit?.();
    }
    lastViewport = viewport;
    lastFitKey = fitKey;
    lastWasFitted = shouldFit;
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
  ["graph-node-scale", "graph-link-scale"].map(byId)
    .forEach((input) => input.addEventListener("change", sync));
  byId("graph-settings-toggle").onclick = () => setPanel(
    byId("graph-settings-panel").hidden, true,
  );
  byId("graph-settings-close").onclick = () => setPanel(false, true);
  byId("graph-settings-panel").addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      setPanel(false, true);
      return;
    }
    if (event.key !== "Tab" || !isSettingsModal()) return;
    const controls = panelControls(event.currentTarget);
    if (!controls.length) return;
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  });
  byId("graph-settings-reset").onclick = () => {
    writeSettings(DEFAULTS);
    sync();
  };
  const compact = window.matchMedia("(max-width: 900px)");
  compact.addEventListener?.("change", sync);
  window.matchMedia(SETTINGS_MODAL_QUERY).addEventListener?.("change", () => {
    setPanel(false, !byId("graph-settings-panel").hidden);
  });
  window.addEventListener("hashchange", () => setPanel(false, false));
  writeSettings(savedSettings());
  setPanel(false, false);
  return {
    sync,
    closePanel(restoreFocus = false) {
      setPanel(false, restoreFocus);
    },
    focusLocal() {
      byId("graph-local-enabled").checked = true;
      byId("graph-local-depth").value = "1";
      sync();
      setPanel(false, false);
    },
  };
}
