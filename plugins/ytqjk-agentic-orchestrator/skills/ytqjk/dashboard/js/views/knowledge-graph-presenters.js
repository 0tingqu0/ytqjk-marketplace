import { byId, button, clear, text } from "../ui/dom.js";

export function displayLabel(node) {
  return node.display_label || node.label || node.title;
}

function nodeType(node) {
  if (node.type === "document") return "知识文档";
  return node.kind === "topic" ? "主题实体" : "知识实体";
}

function detailRows(rows) {
  const list = text("dl", "", "graph-detail-list");
  rows.forEach(([label, value]) => {
    list.append(text("dt", label), text("dd", value));
  });
  return list;
}

function setInspectorKind(kind) {
  const inspector = byId("graph-inspector");
  inspector.hidden = false;
  inspector.dataset.kind = kind;
  inspector.querySelector(".graph-endpoint-actions").hidden = kind !== "node";
  byId("graph-recommendation-section").hidden = kind !== "node";
}

function evidenceRows(evidence) {
  if (!evidence.length) {
    return text("p", "该关系暂无可展示的原文证据。", "graph-empty-copy");
  }
  const list = text("div", "", "graph-evidence-list");
  evidence.forEach((item) => {
    const article = text("article", "", "graph-evidence-card");
    const path = `${item.path || "未知来源"}${
      item.line_start ? `:${item.line_start}` : ""
    }`;
    article.append(text("code", path), text("p", item.excerpt || "暂无摘录"));
    list.append(article);
  });
  return list;
}

export function renderNodeInspector(node, visible) {
  setInspectorKind("node");
  byId("graph-inspector-kicker").textContent = "节点详情";
  byId("graph-inspector-title").textContent = displayLabel(node);
  const copy = node.snippet || node.evidence?.[0]?.excerpt || "暂无摘要。";
  const rows = [detailRows([
    ["类型", nodeType(node)],
    ["范围", node.scope || "跨文档实体"],
    ["提及", node.mentions ? `${node.mentions} 次` : "—"],
    ["文档", node.document_count ? `${node.document_count} 份` : node.path || "—"],
    ["置信度", node.confidence ? `${Math.round(node.confidence * 100)}%` : "—"],
  ]), text("p", copy, "graph-detail-copy")];
  if (!visible) rows.push(text(
    "p", "该结果不在当前图谱视窗中，暂不能设为路径端点。",
    "graph-offcanvas-note",
  ));
  clear(byId("graph-inspector-body"), rows);
  byId("graph-set-source").disabled = !visible;
  byId("graph-set-target").disabled = !visible;
  byId("graph-focus-local").disabled = !visible;
}

export function renderEdgeInspector(edge, nodes) {
  setInspectorKind("edge");
  const source = nodes.get(edge.source);
  const target = nodes.get(edge.target);
  byId("graph-inspector-kicker").textContent = "关系证据";
  byId("graph-inspector-title").textContent = (
    `${displayLabel(source || { label: edge.source })} → ${
      displayLabel(target || { label: edge.target })
    }`
  );
  clear(byId("graph-inspector-body"), [
    detailRows([
      ["关系", edge.label || edge.type],
      ["类型", edge.type],
      ["置信度", `${Math.round(Number(edge.confidence || 0) * 100)}%`],
      ["证据数", `${edge.evidence?.length || 0} 条`],
    ]),
    evidenceRows(edge.evidence || []),
  ]);
}

export function graphResultButton(item, onSelect) {
  const control = button("", "graph-result-item");
  const head = text("span", "", "graph-result-head");
  head.append(
    text("strong", displayLabel(item)),
    text("b", `${Math.round((item.score || 0) * 100)}%`),
  );
  const details = [...(item.reasons || []), item.path].filter(Boolean);
  control.append(head, text("small", details.join(" · ") || "相关知识"));
  control.onclick = () => onSelect(item);
  return control;
}

export function renderGraphSearchResults(response, onSelect) {
  const target = byId("graph-search-results");
  const rows = response.results || [];
  if (!rows.length) {
    const suggestions = response.suggestions?.join("；") || "尝试实体名称。";
    clear(target, [text("p", `没有匹配结果。${suggestions}`, "graph-empty-copy")]);
    return;
  }
  clear(target, rows.map((item) => graphResultButton({
    ...item,
    id: item.node_id,
    label: item.title,
    display_label: item.path ? `${item.title} · ${item.path}` : item.title,
    type: "document",
    reasons: [`${Math.round(item.score * 100)}% 匹配`, "点击定位并聚焦"],
  }, onSelect)));
}
