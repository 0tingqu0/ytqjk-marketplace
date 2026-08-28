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

export function bindRovingGraphNavigation(svg) {
  svg.querySelectorAll(".semantic-node").forEach((node) => {
    node.addEventListener("focus", () => syncRovingTabStop(
      svg, node.dataset.node,
    ));
    node.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        node.click();
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

export function bindGraphNodeDirectory(context, onSelect) {
  const input = byId("graph-node-filter");

  function sync() {
    const { graph, target } = context();
    if (!graph || !target) return;
    const query = normalized(input.value);
    const visible = new Set(
      [...target.querySelectorAll("[data-node]:not(.is-filtered-out)")]
        .map((node) => node.dataset.node),
    );
    const matches = graph.nodes.filter((node) => (
      visible.has(node.id)
      && (!query || normalized([
        node.display_label, node.label, node.path, node.scope,
      ].filter(Boolean).join(" ")).includes(query))
    )).slice(0, 100);
    const rows = matches.map((node) => {
      const control = button(node.display_label || node.label, "graph-node-row");
      control.append(text(
        "small", node.type === "document" ? node.path || "知识文档" : (
          `${node.document_count || 0} 份文档 · ${node.mentions || 0} 次提及`
        ),
      ));
      control.onclick = () => onSelect(node, true);
      return control;
    });
    clear(byId("graph-node-list"), rows.length
      ? rows
      : [text("p", "当前筛选下没有节点。", "graph-empty-copy")]);
    byId("graph-node-directory-summary").textContent = (
      `${matches.length} 个可见节点 · 键盘替代视图`
    );
  }

  input.addEventListener("input", sync);
  byId("graph-node-directory").addEventListener("toggle", (event) => {
    if (event.currentTarget.open) sync();
  });
  byId("graph-node-directory").addEventListener(
    "graphvisibilitychange", (event) => {
      if (event.currentTarget.open) sync();
    },
  );
  return { sync };
}
