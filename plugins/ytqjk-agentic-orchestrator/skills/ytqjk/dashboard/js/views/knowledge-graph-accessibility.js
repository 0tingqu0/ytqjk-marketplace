import { byId, button, clear, text } from "../ui/dom.js";

const DIRECTION = Object.freeze({
  ArrowLeft: [-1, 0],
  ArrowRight: [1, 0],
  ArrowUp: [0, -1],
  ArrowDown: [0, 1],
});

function visibleNodes(svg) {
  if (!svg) return [];
  return [...svg.querySelectorAll(".semantic-node")].filter((node) => (
    !node.classList.contains("is-filtered-out")
  ));
}

function coordinates(node) {
  return {
    x: Number(node.dataset.x || 0),
    y: Number(node.dataset.y || 0),
  };
}

export function nearestDirectionalNode(current, nodes, key) {
  const direction = DIRECTION[key];
  if (!direction) return null;
  const origin = coordinates(current);
  let best = null;
  let bestScore = Number.POSITIVE_INFINITY;
  nodes.forEach((candidate) => {
    if (candidate === current) return;
    const point = coordinates(candidate);
    const dx = point.x - origin.x;
    const dy = point.y - origin.y;
    const primary = dx * direction[0] + dy * direction[1];
    if (primary <= 0) return;
    const perpendicular = Math.abs(dx * direction[1] - dy * direction[0]);
    const score = Math.hypot(dx, dy) + perpendicular * 1.4;
    if (score < bestScore) {
      best = candidate;
      bestScore = score;
    }
  });
  return best;
}

export function syncRovingTabStop(svg, preferredId = "") {
  const nodes = visibleNodes(svg);
  if (!nodes.length) return;
  const active = nodes.find((node) => node.dataset.node === preferredId)
    || nodes.find((node) => node.tabIndex === 0)
    || nodes[0];
  nodes.forEach((node) => node.setAttribute(
    "tabindex", node === active ? "0" : "-1",
  ));
}

export function bindRovingGraphNavigation(svg, onActivate) {
  svg.querySelectorAll(".semantic-node").forEach((node) => {
    node.addEventListener("focus", () => syncRovingTabStop(
      svg, node.dataset.node,
    ));
    node.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        onActivate?.(node.dataset.node);
        return;
      }
      const next = nearestDirectionalNode(node, visibleNodes(svg), event.key);
      if (!next) return;
      event.preventDefault();
      syncRovingTabStop(svg, next.dataset.node);
      next.focus({ preventScroll: true });
    });
  });
  syncRovingTabStop(svg);
}

function normalized(value) {
  return value.trim().toLocaleLowerCase();
}

function displayLabel(node) {
  return node?.display_label || node?.label || node?.id || "未知节点";
}

function limitedGroups(nodes, edges, query, totalLimit) {
  const additional = Math.max(0, totalLimit - 60);
  const firstNodeLimit = (query ? 30 : 36) + Math.ceil(additional / 2);
  const firstEdgeLimit = (query ? 30 : 24) + Math.floor(additional / 2);
  const nodeRows = nodes.slice(0, firstNodeLimit);
  const edgeRows = edges.slice(0, firstEdgeLimit);
  let remaining = totalLimit - nodeRows.length - edgeRows.length;
  if (remaining > 0) {
    nodeRows.push(...nodes.slice(nodeRows.length, nodeRows.length + remaining));
    remaining = totalLimit - nodeRows.length - edgeRows.length;
  }
  if (remaining > 0) {
    edgeRows.push(...edges.slice(edgeRows.length, edgeRows.length + remaining));
  }
  return { nodeRows, edgeRows };
}

function nodeControl(node, onSelect) {
  const control = button(displayLabel(node), "graph-node-row");
  control.dataset.directoryKey = `node:${node.id}`;
  control.append(text(
    "small", node.type === "document" ? node.path || "知识文档" : (
      `${node.document_count || 0} 份文档 · ${node.mentions || 0} 次提及`
    ),
  ));
  control.onclick = () => onSelect(node, true);
  return control;
}

function edgeControl(edge, nodes, onSelect) {
  const source = displayLabel(nodes.get(edge.source));
  const target = displayLabel(nodes.get(edge.target));
  const confidence = Number(edge.confidence);
  const confidenceLabel = Number.isFinite(confidence)
    ? ` · ${Math.round(confidence * 100)}%`
    : "";
  const control = button("", "graph-node-row graph-relation-row");
  control.dataset.directoryKey = `edge:${edge.id}`;
  control.append(
    text("span", `${source} → ${target}`),
    text("small", `${edge.label || edge.type || "关联"}${confidenceLabel}`),
  );
  control.setAttribute(
    "aria-label", `关系：${source} 到 ${target}，${edge.label || edge.type || "关联"}${confidenceLabel}`,
  );
  control.onclick = () => onSelect(edge);
  return control;
}

function directoryHeading(label, count) {
  return text("h4", `${label} · ${count}`, "graph-directory-heading");
}

function visibleGraphIds(target) {
  return {
    nodes: new Set(
      [...target.querySelectorAll("[data-node]:not(.is-filtered-out)")]
        .map((node) => node.dataset.node),
    ),
    edges: new Set(
      [...target.querySelectorAll("[data-edge]:not(.is-filtered-out)")]
        .map((edge) => edge.dataset.edge),
    ),
  };
}

function updateDirectorySummary(visible, shown = null) {
  const shownLabel = shown === null ? " · 展开查看" : ` · 显示 ${shown}`;
  byId("graph-node-directory-summary").textContent = (
    `${visible.nodes.size} 节点 · ${visible.edges.size} 关系${shownLabel}`
  );
}

export function bindGraphNodeDirectory(context, onSelect, onEdgeSelect) {
  const input = byId("graph-node-filter");
  let rowLimit = 60;
  input.previousElementSibling.textContent = "筛选节点或关系";
  input.placeholder = "输入节点、路径或关系名称";

  function sync() {
    const { graph, target } = context();
    if (!graph || !target) return;
    const list = byId("graph-node-list");
    const activeKey = list.contains(document.activeElement)
      ? document.activeElement.dataset.directoryKey || ""
      : "";
    const query = normalized(input.value);
    const visible = visibleGraphIds(target);
    const nodes = new Map(graph.nodes.map((node) => [node.id, node]));
    const matchingNodes = graph.nodes.filter((node) => (
      visible.nodes.has(node.id)
      && (!query || normalized([
        node.display_label, node.label, node.path, node.scope,
      ].filter(Boolean).join(" ")).includes(query))
    ));
    const matchingEdges = graph.edges.filter((edge) => (
      visible.edges.has(edge.id)
      && (!query || normalized([
        edge.label, edge.type, displayLabel(nodes.get(edge.source)),
        displayLabel(nodes.get(edge.target)),
      ].filter(Boolean).join(" ")).includes(query))
    ));
    const { nodeRows, edgeRows } = limitedGroups(
      matchingNodes, matchingEdges, query, rowLimit,
    );
    const rows = [];
    if (nodeRows.length) {
      rows.push(directoryHeading("节点", nodeRows.length));
      rows.push(...nodeRows.map((node) => nodeControl(node, onSelect)));
    }
    if (edgeRows.length) {
      rows.push(directoryHeading("关系", edgeRows.length));
      rows.push(...edgeRows.map((edge) => edgeControl(
        edge, nodes, onEdgeSelect,
      )));
    }
    const shown = nodeRows.length + edgeRows.length;
    const remaining = matchingNodes.length + matchingEdges.length - shown;
    if (remaining > 0) {
      const more = button(`显示更多（剩余 ${remaining}）`, "secondary graph-directory-more");
      more.onclick = () => {
        rowLimit += 60;
        sync();
        (list.querySelector(".graph-directory-more")
          || list.querySelector(".graph-node-row:last-of-type"))?.focus();
      };
      rows.push(more);
    }
    clear(list, rows.length
      ? rows
      : [text("p", "当前筛选下没有节点或关系。", "graph-empty-copy")]);
    updateDirectorySummary(visible, shown);
    if (activeKey) {
      const replacement = [...list.querySelectorAll("[data-directory-key]")]
        .find((control) => control.dataset.directoryKey === activeKey);
      (replacement || byId("graph-node-directory-summary").closest("summary"))
        .focus({ preventScroll: true });
    }
  }

  input.addEventListener("input", () => {
    rowLimit = 60;
    sync();
  });
  byId("graph-node-directory").addEventListener("toggle", (event) => {
    if (event.currentTarget.open) sync();
  });
  byId("graph-node-directory").addEventListener(
    "graphvisibilitychange", (event) => {
      if (event.currentTarget.open) {
        rowLimit = 60;
        sync();
        return;
      }
      const { target } = context();
      if (target) updateDirectorySummary(visibleGraphIds(target));
    },
  );
  return {
    sync() {
      rowLimit = 60;
      sync();
    },
  };
}
