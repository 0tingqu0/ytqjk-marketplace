import { api } from "../api.js";
import { byId, clear, text } from "./dom.js";

const LABELS = {
  attach: "挂到父库",
  create: "新建知识库",
  detach: "拆卸知识库",
  insert_between: "插入中间库",
  move: "移动知识库",
};

let context = null;
let issued = null;
let onCommitted = () => {};

function field(id, visible) {
  const wrapper = byId(id);
  wrapper.hidden = !visible;
  const control = wrapper.querySelector("input, select");
  if (control) control.disabled = !visible;
}

function subtreeIds(tree, rootId) {
  const found = new Set([rootId]);
  let changed = true;
  while (changed) {
    changed = false;
    tree.nodes.forEach((node) => {
      if (node.parent_id && found.has(node.parent_id)
          && !found.has(node.id)) {
        found.add(node.id);
        changed = true;
      }
    });
  }
  return found;
}

function fillSelect(select, nodes, selected = "", root = false) {
  const options = nodes.map((node) => {
    const option = document.createElement("option");
    option.value = node.id;
    option.textContent = `${node.title} · ${node.id}`;
    option.selected = node.id === selected;
    return option;
  });
  if (root) {
    const option = document.createElement("option");
    option.value = "";
    option.textContent = "不挂父库（作为根节点）";
    option.selected = selected === "";
    options.unshift(option);
  } else if (!options.length) {
    const option = document.createElement("option");
    option.value = "";
    option.textContent = "没有可用节点";
    option.disabled = true;
    option.selected = true;
    options.push(option);
  }
  clear(select, options);
}

function parentChoices(action, node, tree) {
  const excluded = node ? subtreeIds(tree, node.id) : new Set();
  const choices = tree.nodes.filter((item) => !excluded.has(item.id));
  const selected = action === "create" ? node?.id || "" : "";
  fillSelect(byId("tree-parent-id"), choices, selected,
    action === "create");
}

function middleChoices(node, tree) {
  const excluded = subtreeIds(tree, node.id);
  const choices = tree.nodes.filter((item) => (
    item.parent_id === null
    && item.id !== node.parent_id
    && !excluded.has(item.id)
  ));
  fillSelect(byId("tree-middle-id"), choices);
}

function resetPreview() {
  issued = null;
  byId("tree-preview").hidden = true;
  byId("tree-action-submit").textContent = "生成变更预览";
  byId("tree-action-status").textContent = "";
}

function configure(action, node, tree) {
  const creating = action === "create";
  field("tree-node-id-field", creating);
  field("tree-title-field", creating);
  field("tree-node-type-field", creating);
  field("tree-parent-id-field", creating || action === "attach"
    || action === "move");
  field("tree-middle-id-field", action === "insert_between");
  const mounted = creating && byId("tree-node-type").value === "mounted";
  field("tree-mount-id-field", mounted);
  field("tree-capability-field", mounted);
  if (creating || action === "attach" || action === "move") {
    parentChoices(action, node, tree);
  }
  if (action === "insert_between") middleChoices(node, tree);
}

function createPayload(node) {
  const kind = byId("tree-node-type").value;
  return {
    node_id: byId("tree-node-id").value.trim(),
    title: byId("tree-title").value.trim(),
    type: kind,
    parent_id: byId("tree-parent-id").value || null,
    metadata: kind === "mounted" ? {
      mount_id: byId("tree-mount-id").value.trim(),
      capability: byId("tree-capability").value.trim(),
    } : {},
  };
}

function payload() {
  const { action, node } = context;
  if (action === "create") return createPayload(node);
  if (action === "detach") return { node_id: node.id };
  if (action === "insert_between") return {
    parent_id: node.parent_id,
    node_id: node.id,
    middle_id: byId("tree-middle-id").value,
  };
  return {
    node_id: node.id,
    parent_id: byId("tree-parent-id").value,
  };
}

function renderPreview(preview) {
  const summary = preview.summary;
  const items = [
    `动作：${LABELS[preview.action]}`,
    `影响节点：${preview.affected_nodes.join("、")}`,
    `子树规模：${summary.subtree_size}`,
    `新链路：${summary.new_chain.join(" → ") || "根节点"}`,
    `锚点影响：${summary.anchor_impact}`,
  ];
  clear(byId("tree-preview-list"), items.map((item) => text("li", item)));
  byId("tree-preview").hidden = false;
  byId("tree-action-submit").textContent = "确认并执行";
  byId("tree-action-status").textContent = "请核对预览后再次确认。";
}

async function submit(event) {
  event.preventDefault();
  const submitButton = byId("tree-action-submit");
  submitButton.disabled = true;
  try {
    if (!issued) {
      const result = await api.treePreview(context.action, payload());
      issued = result.preview;
      renderPreview(issued);
      return;
    }
    const result = await api.treeCommit(context.action, {
      digest: issued.digest,
      expected_revision: issued.expected_revision,
    });
    onCommitted(result.tree);
    byId("tree-dialog").close();
  } catch (error) {
    resetPreview();
    byId("tree-action-status").textContent = `操作失败：${error.message}`;
  } finally {
    submitButton.disabled = false;
  }
}

export function openTreeAction(action, node, tree) {
  context = { action, node, tree };
  const form = byId("tree-action-form");
  form.reset();
  byId("tree-action-title").textContent = LABELS[action];
  byId("tree-action-target").textContent = node
    ? `当前节点：${node.title} · ${node.id}`
    : "创建新的根知识库，也可在下方选择父库。";
  configure(action, node, tree);
  resetPreview();
  const dialog = byId("tree-dialog");
  if (!dialog.open) dialog.showModal();
}

export function bindTreeDialog(callback) {
  onCommitted = callback;
  const form = byId("tree-action-form");
  form.onsubmit = submit;
  form.oninput = (event) => {
    if (!context) return;
    if (event.target.id === "tree-node-type") {
      const mounted = event.target.value === "mounted";
      field("tree-mount-id-field", mounted);
      field("tree-capability-field", mounted);
    }
    resetPreview();
  };
  byId("close-tree-dialog").onclick = () => byId("tree-dialog").close();
  byId("cancel-tree-action").onclick = () => byId("tree-dialog").close();
  byId("tree-dialog").onclick = (event) => {
    if (event.target === byId("tree-dialog")) byId("tree-dialog").close();
  };
}
