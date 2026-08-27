import { byId, clear, text } from "../ui/dom.js";

const TYPE_LABELS = {
  global: "个人根库",
  group: "分组库",
  mounted: "挂载库",
  project: "项目库",
};

function displayTitle(node) {
  return node.id === "global" ? "个人总库" : (node.title || node.id);
}

function checkedInputs() {
  return byId("peer-export-node-ids").querySelectorAll(
    'input[type="checkbox"][data-library-id]:checked',
  );
}

export function selectedLocalLibraryIds() {
  return Array.from(checkedInputs(), (input) => input.dataset.libraryId);
}

export function selectLocalLibraryIds(values) {
  const selected = new Set((values || []).filter(Boolean));
  byId("peer-export-node-ids").querySelectorAll(
    'input[type="checkbox"][data-library-id]',
  ).forEach((input) => {
    input.checked = !input.disabled && selected.has(input.dataset.libraryId);
    input.closest("[role=treeitem]")?.setAttribute(
      "aria-selected", String(input.checked),
    );
  });
}

function hierarchy(nodes) {
  const byNodeId = new Map(nodes.map((node) => [node.id, node]));
  const children = new Map();
  nodes.forEach((node) => {
    if (!node.parent_id || !byNodeId.has(node.parent_id)) return;
    const items = children.get(node.parent_id) || [];
    items.push(node);
    children.set(node.parent_id, items);
  });
  children.forEach((items) => items.sort(
    (left, right) => displayTitle(left).localeCompare(
      displayTitle(right), "zh-CN",
    ),
  ));
  return { byNodeId, children };
}

function nodeLevel(node, byNodeId) {
  let level = 1;
  let current = node;
  const visited = new Set([node.id]);
  while (current.parent_id) {
    current = byNodeId.get(current.parent_id);
    if (!current || visited.has(current.id)) return null;
    visited.add(current.id);
    level += 1;
  }
  return level;
}

function descendants(projectId, children) {
  const found = new Set();
  const pending = [projectId];
  while (pending.length) {
    const nodeId = pending.shift();
    if (found.has(nodeId)) continue;
    found.add(nodeId);
    pending.push(...(children.get(nodeId) || []).map((node) => node.id));
  }
  return found;
}

function canExport(node, level, projectId, projectLevel, projectScope) {
  if (level === null || projectLevel === null) return false;
  return node.type !== "mounted" && (
    level === projectLevel || (
      projectScope.has(node.id)
      && (node.id === projectId || node.type !== "project")
    )
  );
}

function disabledReason(node, level, projectLevel) {
  if (node.type === "mounted") return "挂载库不能再次开放";
  if (level === null || projectLevel === null) return "层级信息不完整";
  return `当前项目为 ${projectLevel} 级，只能开放同级库或本项目子库`;
}

function renderNode(node, context, level) {
  const nested = context.children.get(node.id) || [];
  const selectable = canExport(
    node, level, context.projectId, context.projectLevel,
    context.projectScope,
  );
  const item = document.createElement("details");
  item.className = `peer-library-node${nested.length ? "" : " is-leaf"}`;
  item.open = level <= 2;
  item.setAttribute("role", "treeitem");
  item.setAttribute("aria-level", String(level));
  item.setAttribute("aria-selected", "false");
  const summary = document.createElement("summary");
  const label = text("label", "", "peer-library-label");
  const input = document.createElement("input");
  input.type = "checkbox";
  input.dataset.libraryId = node.id;
  input.disabled = !selectable;
  input.title = selectable ? "允许对方访问此库" : disabledReason(
    node, level, context.projectLevel,
  );
  input.onclick = (event) => event.stopPropagation();
  input.onchange = () => item.setAttribute(
    "aria-selected", String(input.checked),
  );
  const folder = text("span", "", "peer-folder-icon");
  folder.setAttribute("aria-hidden", "true");
  const identity = text("span", "", "peer-library-identity");
  identity.append(
    text("b", displayTitle(node)),
    text("small", `${TYPE_LABELS[node.type] || node.type} · ${node.id}`),
  );
  label.append(input, folder, identity, text("span", `${level} 级`, "peer-level"));
  label.onclick = (event) => {
    event.stopPropagation();
    if (event.target === input) return;
    event.preventDefault();
    if (nested.length) item.open = !item.open;
  };
  summary.append(label);
  item.append(summary);
  if (nested.length) {
    const group = text("div", "", "peer-library-children");
    group.setAttribute("role", "group");
    nested.forEach((child) => group.append(
      renderNode(child, context, level + 1),
    ));
    item.append(group);
  }
  return item;
}

export function renderLocalLibraryTree(tree, projectId) {
  const control = byId("peer-export-node-ids");
  const selected = selectedLocalLibraryIds();
  const nodes = tree?.nodes || [];
  const { byNodeId, children } = hierarchy(nodes);
  const project = byNodeId.get(projectId);
  const projectLevel = project ? nodeLevel(project, byNodeId) : null;
  const roots = nodes.filter(
    (node) => !node.parent_id || !byNodeId.has(node.parent_id),
  ).sort((left, right) => displayTitle(left).localeCompare(
    displayTitle(right), "zh-CN",
  ));
  const context = {
    children,
    projectId,
    projectLevel,
    projectScope: descendants(projectId, children),
  };
  clear(control, roots.length ? roots.map(
    (root) => renderNode(root, context, nodeLevel(root, byNodeId) || 1),
  ) : [text("p", "尚无可开放的本机知识库。", "empty-state")]);
  selectLocalLibraryIds(selected);
  control.dataset.projectLevel = String(projectLevel ?? "");
}
