import { byId, clear, text } from "../ui/dom.js";
import { bindGraphExplorer } from "./knowledge-graph-explorer.js";
import { bindGraphNodeDirectory } from "./knowledge-graph-accessibility.js";
import { renderLibraryTopology } from "./knowledge-graph.js";
import {
  applyGraphHighlight,
  renderSemanticGraph,
} from "./semantic-graph-render.js";
import {
  graphResultButton,
  renderEdgeInspector,
  renderGraphSearchResults,
  renderNodeInspector,
} from "./knowledge-graph-presenters.js";

let client = null;
const runtime = {
  target: null,
  snapshot: null,
  graph: null,
  error: "",
  mode: "semantic",
  selected: null,
  selectedEdge: null,
  pathSource: null,
  pathTarget: null,
  pathNodes: new Set(),
  pathEdges: new Set(),
  pathRequest: 0,
  viewport: null,
  explorer: null,
  directory: null,
};

function graphNodes() {
  return new Map((runtime.graph?.nodes || []).map((node) => [node.id, node]));
}

function modeButtons() {
  document.querySelectorAll("[data-graph-mode]").forEach((control) => {
    control.setAttribute(
      "aria-pressed", String(control.dataset.graphMode === runtime.mode),
    );
  });
  byId("graph-workbench").dataset.mode = runtime.mode;
}

function updateZoomControls(state = null) {
  const semantic = runtime.mode === "semantic" && state;
  byId("graph-zoom-in").disabled = !semantic || !state.canZoomIn;
  byId("graph-zoom-out").disabled = !semantic || !state.canZoomOut;
  byId("graph-zoom-reset").disabled = !semantic;
  byId("graph-zoom-level").textContent = `${state?.percent || 100}%`;
}

function emptyGraph(message) {
  const empty = text("div", "", "graph-empty-state");
  const title = text("strong", "暂时无法生成语义图谱");
  empty.append(title, text("p", message));
  clear(runtime.target, [empty]);
}

function renderMode() {
  if (!runtime.target) return;
  modeButtons();
  if (!runtime.snapshot) {
    clear(runtime.target, [text("p", "正在读取知识图谱…", "topology-loading")]);
    return;
  }
  if (runtime.mode === "topology") {
    runtime.viewport = null;
    updateZoomControls();
    renderLibraryTopology(runtime.target, runtime.snapshot);
    return;
  }
  if (!runtime.graph) {
    emptyGraph(runtime.error || "索引中尚无可用知识，批准资料后可再次刷新。");
    return;
  }
  runtime.viewport = renderSemanticGraph(
    runtime.target, runtime.graph, selectNode, selectEdge, updateZoomControls,
  );
  applyHighlight();
  runtime.explorer?.sync();
  runtime.directory?.sync();
  updateOptions();
}

function selectNode(node, focus = false) {
  const visible = graphNodes().has(node.id);
  runtime.selected = node;
  runtime.selectedEdge = null;
  renderNodeInspector(node, visible);
  applyHighlight();
  if (focus && visible) runtime.viewport?.focus(node.id);
  runtime.explorer?.sync();
  loadRecommendations(node.id);
}

function selectEdge(edge) {
  runtime.selectedEdge = edge;
  renderEdgeInspector(edge, graphNodes());
  applyHighlight();
}

function applyHighlight() {
  if (!runtime.target || runtime.mode !== "semantic") return;
  applyGraphHighlight(
    runtime.target,
    runtime.selected?.id || "",
    runtime.selectedEdge?.id || "",
    runtime.pathNodes,
    runtime.pathEdges,
  );
}

async function loadRecommendations(nodeId) {
  const target = byId("graph-recommendations");
  clear(target, [text("p", "正在计算相似知识…", "muted")]);
  try {
    const response = await client.knowledgeRecommendations(nodeId, 6);
    if (runtime.selected?.id !== nodeId) return;
    const rows = response.results || [];
    clear(target, rows.length
      ? rows.map((item) => graphResultButton(item, (selected) => selectNode(
        graphNodes().get(selected.id || selected.node_id) || selected, true,
      )))
      : [text("p", "暂无达到阈值的相似知识。", "graph-empty-copy")]);
  } catch (error) {
    clear(target, [text("p", `推荐失败：${error.message}`, "graph-error-copy")]);
  }
}

function updateOptions() {
  const options = byId("graph-search-options");
  clear(options, (runtime.graph?.nodes || []).slice(0, 100).map((node) => {
    const option = document.createElement("option");
    option.value = node.label;
    return option;
  }));
  const mode = runtime.graph?.capabilities?.semantic_search;
  byId("graph-search-mode").textContent = mode === "hybrid-vector"
    ? "向量 + 概念混合召回"
    : "概念相似度召回（向量索引未启用）";
}

async function search(event) {
  event.preventDefault();
  const input = byId("graph-search-input");
  const query = input.value.trim();
  if (!query) {
    byId("graph-search-status").textContent = "请输入概念、实体或问题。";
    input.focus();
    return;
  }
  const submit = byId("graph-search-submit");
  submit.disabled = true;
  byId("graph-search-status").textContent = "正在检索本地知识…";
  try {
    const response = await client.semanticSearch(query, 8);
    renderGraphSearchResults(response, (item) => selectNode(
      graphNodes().get(item.id || item.node_id) || item, true,
    ));
    byId("graph-search-status").textContent = response.results.length
      ? `找到 ${response.results.length} 条结果`
      : "未找到匹配知识，可尝试缩短检索词。";
  } catch (error) {
    byId("graph-search-status").textContent = `检索失败：${error.message}`;
  } finally {
    submit.disabled = false;
  }
}

function setPathEndpoint(kind) {
  if (!runtime.selected) return;
  runtime[kind] = runtime.selected;
  byId(kind === "pathSource" ? "graph-path-source" : "graph-path-target")
    .textContent = runtime.selected.label;
  byId("graph-run-path").disabled = !runtime.pathSource || !runtime.pathTarget;
}

async function runPath() {
  if (!runtime.pathSource || !runtime.pathTarget) return;
  const sourceId = runtime.pathSource.id;
  const targetId = runtime.pathTarget.id;
  const requestId = ++runtime.pathRequest;
  const status = byId("graph-path-status");
  status.textContent = "正在探索最短知识路径…";
  try {
    const response = await client.knowledgePath(
      sourceId, targetId, 5,
    );
    if (requestId !== runtime.pathRequest
        || runtime.pathSource?.id !== sourceId
        || runtime.pathTarget?.id !== targetId) return;
    runtime.pathNodes = new Set((response.nodes || []).map((node) => node.id));
    runtime.pathEdges = new Set((response.edges || []).map((edge) => edge.id));
    applyHighlight();
    status.textContent = response.found
      ? `${response.nodes.map((node) => (
        `${node.type === "document" ? "文档" : "实体"}：${node.label}`
      )).join(" → ")}（${response.hops} 跳）`
      : "在 5 跳范围内没有找到连接路径。";
  } catch (error) {
    status.textContent = `路径探索失败：${error.message}`;
  }
}

function clearPath() {
  runtime.pathRequest += 1;
  runtime.pathSource = null;
  runtime.pathTarget = null;
  runtime.pathNodes = new Set();
  runtime.pathEdges = new Set();
  byId("graph-path-source").textContent = "未选择";
  byId("graph-path-target").textContent = "未选择";
  byId("graph-path-status").textContent = "选择节点后设为起点或终点。";
  byId("graph-run-path").disabled = true;
  applyHighlight();
}

function reconcileGraphState(graph) {
  const nodes = new Map((graph?.nodes || []).map((node) => [node.id, node]));
  const edges = new Set((graph?.edges || []).map((edge) => edge.id));
  runtime.pathRequest += 1;
  runtime.selected = nodes.get(runtime.selected?.id) || null;
  runtime.selectedEdge = (graph?.edges || []).find(
    (edge) => edge.id === runtime.selectedEdge?.id,
  ) || null;
  runtime.pathSource = nodes.get(runtime.pathSource?.id) || null;
  runtime.pathTarget = nodes.get(runtime.pathTarget?.id) || null;
  runtime.pathNodes = new Set([...runtime.pathNodes].filter((id) => nodes.has(id)));
  runtime.pathEdges = new Set([...runtime.pathEdges].filter((id) => edges.has(id)));
  byId("graph-inspector").hidden = !runtime.selected && !runtime.selectedEdge;
  byId("graph-set-source").disabled = !runtime.selected;
  byId("graph-set-target").disabled = !runtime.selected;
  byId("graph-path-source").textContent = runtime.pathSource?.label || "未选择";
  byId("graph-path-target").textContent = runtime.pathTarget?.label || "未选择";
  byId("graph-run-path").disabled = !runtime.pathSource || !runtime.pathTarget;
}

export function bindKnowledgeGraphWorkbench(api) {
  client = api;
  runtime.explorer = bindGraphExplorer(() => ({ target: runtime.target, graph: runtime.graph, selectedId: runtime.selected?.id, viewport: runtime.viewport }));
  runtime.directory = bindGraphNodeDirectory(
    () => ({ target: runtime.target, graph: runtime.graph }),
    selectNode,
  );
  document.querySelectorAll("[data-graph-mode]").forEach((control) => {
    control.onclick = () => {
      runtime.mode = control.dataset.graphMode;
      renderMode();
    };
  });
  byId("graph-search-form").onsubmit = search;
  byId("graph-search-clear").onclick = () => {
    byId("graph-search-input").value = "";
    byId("graph-search-status").textContent = "";
    clear(byId("graph-search-results"));
  };
  byId("graph-set-source").onclick = () => setPathEndpoint("pathSource");
  byId("graph-set-target").onclick = () => setPathEndpoint("pathTarget");
  byId("graph-focus-local").onclick = () => runtime.explorer?.focusLocal();
  byId("graph-inspector-close").onclick = () => { byId("graph-inspector").hidden = true; };
  byId("graph-run-path").onclick = runPath;
  byId("graph-clear-path").onclick = clearPath;
  byId("graph-zoom-in").onclick = () => runtime.viewport?.zoomIn();
  byId("graph-zoom-out").onclick = () => runtime.viewport?.zoomOut();
  byId("graph-zoom-reset").onclick = () => runtime.viewport?.reset();
}

export function renderKnowledgeGraphWorkbench(target, snapshot, graph, error) {
  const targetChanged = runtime.target !== target;
  const snapshotChanged = runtime.snapshot !== snapshot;
  const graphChanged = runtime.graph !== graph;
  const errorChanged = runtime.error !== error;
  if (graphChanged) reconcileGraphState(graph);
  runtime.target = target;
  runtime.snapshot = snapshot;
  runtime.graph = graph;
  runtime.error = error;
  const needsRender = targetChanged || (runtime.mode === "semantic"
    ? graphChanged || (!graph && errorChanged)
    : snapshotChanged);
  if (!needsRender) return;
  renderMode();
}
