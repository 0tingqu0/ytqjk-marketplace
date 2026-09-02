import { api } from "../api.js";
import { byId, button, clear, formatBytes, icon, text } from "../ui/dom.js";
import { confirmAction } from "../ui/confirm.js";

const TYPE_LABELS = {
  global: "全局库",
  group: "分组库",
  mounted: "挂载库",
  project: "项目库",
};

function libraryFacts(node) {
	const stats = node.stats;
	return text(
		"span",
		`资料索引 ${stats.indexed_documents}/${stats.total_documents} 文件 · `
		+ `${stats.indexed_chunks}/${stats.total_chunks} 分块；`
		+ `占用 ${formatBytes(stats.used_bytes)}/${formatBytes(node.capacity_bytes)}`,
		"muted",
	);
}

function action(label, name, node, tree, openAction) {
  const control = button(label, "secondary tree-action");
  control.onclick = () => openAction(name, node, tree);
  return control;
}

function actionMenu(node, controls) {
  const menu = document.createElement("details");
  menu.className = "tree-action-menu";
  const trigger = document.createElement("summary");
  trigger.title = "更多操作";
  trigger.setAttribute("aria-label", `${node.title}：更多操作`);
  trigger.append(icon("ph-dots-three"));
  controls.addEventListener("click", (event) => {
    if (event.target.closest("button")) menu.open = false;
  });
  menu.addEventListener("toggle", () => {
    if (!menu.open) return;
    document.querySelectorAll(".tree-action-menu[open]").forEach((item) => {
      if (item !== menu) item.open = false;
    });
  });
  menu.append(trigger, controls);
  return menu;
}

function renderNode(node, children, projects, tree, handlers, depth) {
  const item = document.createElement("details");
  item.className = "tree-node";
  item.open = depth < 2;
  const summary = document.createElement("summary");
  const identity = text("span", "", "tree-node-identity");
  identity.append(
    text("b", node.title),
    text("small", `${TYPE_LABELS[node.type] || node.type} · ${node.id}`),
  );
	summary.append(identity, libraryFacts(node));
  item.append(summary);

  const controls = text("div", "", "tree-node-actions");
	if (node.type === "project" && projects.has(node.id)) {
		const open = button("打开索引", "secondary tree-action");
		open.onclick = () => handlers.openProject(projects.get(node.id), node);
		controls.append(open);
	}
  controls.append(
    action("新建子库", "create", node, tree, handlers.openAction),
  );
  if (node.parent_id === null) {
    controls.append(
      action("挂到父库", "attach", node, tree, handlers.openAction),
    );
  } else {
    controls.append(
      action("移动", "move", node, tree, handlers.openAction),
      action(
        "插入中间库", "insert_between", node, tree,
        handlers.openAction,
      ),
      action("拆卸", "detach", node, tree, handlers.openAction),
    );
  }
  item.append(actionMenu(node, controls));

  const nested = children.get(node.id) || [];
  if (nested.length) {
    const branch = text("div", "", "tree-children");
    nested.forEach((child) => branch.append(
      renderNode(
        child, children, projects, tree, handlers, depth + 1,
      ),
    ));
    item.append(branch);
  }
  return item;
}

function renderFallback(snapshot, target) {
  const projects = snapshot?.projects || [];
  if (!projects.length) {
    clear(target, [
      text("p", "尚无可展示的项目子库。", "empty-state"),
    ]);
    return;
  }
  clear(target, projects.map((project) => {
    const card = text("article", "", "surface project-card");
    card.append(
      text("h3", project.name),
      text("p", project.id, "muted"),
		text("span", "知识库树统计暂不可用", "muted"),
    );
    return card;
  }));
}

export function renderLibraries(snapshot, tree, treeError, handlers) {
  const target = byId("project-grid");
  const status = byId("tree-status");
  byId("new-root-library").disabled = !tree;
  if (!tree) {
    status.textContent = treeError
      ? `知识库树不可用：${treeError}`
      : "知识库树尚未配置：NOT_CONFIGURED";
    renderFallback(snapshot, target);
    return;
  }
  const nodes = new Map(tree.nodes.map((node) => [node.id, node]));
  const children = new Map();
  tree.nodes.forEach((node) => {
    if (node.parent_id === null || !nodes.has(node.parent_id)) return;
    const items = children.get(node.parent_id) || [];
    items.push(node);
    children.set(node.parent_id, items);
  });
  children.forEach((items) => items.sort(
    (a, b) => a.title.localeCompare(b.title, "zh-CN"),
  ));
  const projects = new Map(
    (snapshot?.projects || []).map((item) => [item.id, item]),
  );
  const roots = tree.roots.map((id) => nodes.get(id)).filter(Boolean);
  status.textContent = [
    `修订 ${tree.revision}`,
    `${tree.nodes.length} 个知识库`,
    `${roots.length} 个根节点`,
  ].join(" · ");
  const forest = text("div", "", "knowledge-forest");
  roots.forEach((root) => forest.append(
    renderNode(root, children, projects, tree, handlers, 0),
  ));
  clear(target, roots.length
    ? [forest]
    : [text(
      "p", "知识库树没有根节点，已阻止渲染。", "empty-state",
    )]);
}
