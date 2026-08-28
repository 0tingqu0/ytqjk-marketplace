import { byId, button, clear, text } from "../ui/dom.js";
import { renderLibraryTopology } from "./knowledge-graph.js";
import {
  applyGraphHighlight,
  renderSemanticGraph,
} from "./semantic-graph-render.js";

let client = null;
const runtime = {
  target: null,
  snapshot: null,
  graph: null,
  error: "",
  mode: "semantic",
  selected: null,
  pathSource: null,
  pathTarget: null,
  pathNodes: new Set(),
  pathEdges: new Set(),
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
    renderLibraryTopology(runtime.target, runtime.snapshot);
    return;
  }
  if (!runtime.graph) {
    emptyGraph(runtime.error || "索引中尚无可用知识，批准资料后可再次刷新。");
    return;
  }
  renderSemanticGraph(runtime.target, runtime.graph, selectNode);
  applyHighlight();
  updateOptions();
}

function nodeType(node) {
  if (node.type === "document") return "知识文档";
  return node.kind === "topic" ? "主题实体" : "知识实体";
}

function detailRows(node) {
  const list = text("dl", "", "graph-detail-list");
  const rows = [
    ["类型", nodeType(node)],
    ["范围", node.scope || "跨文档实体"],
    ["提及", node.mentions ? `${node.mentions} 次` : "—"],
    ["文档", node.document_count ? `${node.document_count} 份` : node.path || "—"],
  ];
  rows.forEach(([label, value]) => {
    list.append(text("dt", label), text("dd", value));
  });
  return list;
}

function selectNode(node) {
  runtime.selected = node;
  byId("graph-inspector").hidden = false;
  byId("graph-inspector-title").textContent = node.label;
  const body = byId("graph-inspector-body");
  const copy = node.snippet || node.evidence?.[0]?.excerpt || "暂无摘要。";
  clear(body, [detailRows(node), text("p", copy, "graph-detail-copy")]);
  byId("graph-set-source").disabled = false;
  byId("graph-set-target").disabled = false;
  applyHighlight();
  loadRecommendations(node.id);
}

function applyHighlight() {
  if (!runtime.target || runtime.mode !== "semantic") return;
  applyGraphHighlight(
    runtime.target,
    runtime.selected?.id || "",
    runtime.pathNodes,
    runtime.pathEdges,
  );
}

function relatedButton(item) {
  const control = button("", "graph-result-item");
  const head = text("span", "", "graph-result-head");
  head.append(
    text("strong", item.label || item.title),
    text("b", `${Math.round((item.score || 0) * 100)}%`),
  );
  const details = [...(item.reasons || []), item.path].filter(Boolean);
  const reason = details.join(" · ") || "相关知识";
  control.append(head, text("small", reason));
  control.onclick = () => selectNode(graphNodes().get(item.id || item.node_id) || item);
  return control;
}

async function loadRecommendations(nodeId) {
  const target = byId("graph-recommendations");
  clear(target, [text("p", "正在计算相似知识…", "muted")]);
  try {
    const response = await client.knowledgeRecommendations(nodeId, 6);
    if (runtime.selected?.id !== nodeId) return;
    const rows = response.results || [];
    clear(target, rows.length
      ? rows.map(relatedButton)
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

function renderSearchResults(response) {
  const target = byId("graph-search-results");
  const rows = response.results || [];
  if (!rows.length) {
    const suggestions = response.suggestions?.join("；") || "尝试实体名称。";
    clear(target, [text("p", `没有匹配结果。${suggestions}`, "graph-empty-copy")]);
    return;
  }
  clear(target, rows.map((item) => relatedButton({
    ...item,
    id: item.node_id,
    label: item.title,
    type: "document",
    reasons: [`${Math.round(item.score * 100)}% 匹配`],
  })));
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
    renderSearchResults(response);
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
  const status = byId("graph-path-status");
  status.textContent = "正在探索最短知识路径…";
  try {
    const response = await client.knowledgePath(
      runtime.pathSource.id, runtime.pathTarget.id, 5,
    );
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

export function bindKnowledgeGraphWorkbench(api) {
  client = api;
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
  byId("graph-run-path").onclick = runPath;
  byId("graph-clear-path").onclick = clearPath;
}

export function renderKnowledgeGraphWorkbench(target, snapshot, graph, error) {
  runtime.target = target;
  runtime.snapshot = snapshot;
  runtime.graph = graph;
  runtime.error = error;
  renderMode();
}
