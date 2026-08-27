import { byId, button, clear, text } from "../ui/dom.js";

function settings(state) {
  return state.peer?.peer_service || null;
}

function value(id, next) {
  const control = byId(id);
  if (control.type === "checkbox") control.checked = Boolean(next);
  else control.value = String(next ?? "");
}

function peerButton(label, action, peerId, className = "secondary") {
  const control = button(label, className);
  control.dataset.peerAction = action;
  control.dataset.peerId = peerId;
  return control;
}

function renderService(state, service) {
  const configured = Boolean(service);
  const runtime = state.peer?.runtime?.status || "UNKNOWN";
  byId("peer-state").textContent = configured ? "已配置" : "未配置";
  byId("peer-state").className = `status-pill ${configured ? "ready" : ""}`;
  byId("peer-runtime").textContent = `运行状态：${runtime}`;
  byId("peer-bootstrap").hidden = configured;
  byId("peer-new").disabled = !configured;
  byId("peer-secret").disabled = !configured;
  const form = byId("peer-service-form");
  form.querySelectorAll("input, button").forEach((item) => {
    item.disabled = !configured;
  });
  if (configured && form.dataset.dirty !== "true") {
    value("peer-bind-host", service.bind_host);
    value("peer-port", service.port);
    value("peer-service-enabled", service.enabled);
    value("peer-service-insecure", service.allow_insecure_lan);
  }
}

function peerCard(peer, state) {
  const card = text("article", "", "peer-card");
  const head = document.createElement("header");
  head.append(
    text("h3", peer.title),
    text("span", peer.enabled ? "已启用" : "已停用", "status-pill"),
  );
  const facts = document.createElement("dl");
  const rows = [
    ["连接标识", peer.peer_id],
    ["项目", peer.project_id],
    ["地址", peer.endpoint],
    ["访问对方库", peer.remote_node_id],
    ["开放本机库", (peer.export_node_ids || [peer.export_node_id])
      .filter(Boolean).join("、")],
    ["密钥指纹", peer.key_fingerprint],
  ];
  rows.forEach(([label, detail]) => facts.append(
    text("dt", label), text("dd", detail || "未配置"),
  ));
  const health = state.peerHealth.get(peer.peer_id);
  if (health) facts.append(text("dt", "健康检查"), text("dd", health));
  const actions = text("div", "", "peer-actions");
  actions.append(
    peerButton("检查连接", "health", peer.peer_id),
    peerButton("编辑", "edit", peer.peer_id),
    peerButton("删除", "remove", peer.peer_id, "danger"),
  );
  card.append(head, facts, actions);
  return card;
}

function renderPeers(state, service) {
  const peers = service?.peers || [];
  clear(byId("peer-list"), peers.length
    ? peers.map((peer) => peerCard(peer, state))
    : [text("p", service
      ? "尚未授权其他电脑。"
      : "先初始化局域网服务。", "empty-state")]);
}

function projectOptions(snapshot) {
  return (snapshot?.projects || []).map((project) => {
    const option = document.createElement("option");
    option.value = project.id;
    option.textContent = project.name || project.id;
    option.title = project.id;
    return option;
  });
}

function renderProjectSelect(id, snapshot) {
  const control = byId(id);
  const selected = control.value;
  const options = projectOptions(snapshot);
  clear(control, options);
  if (options.some((option) => option.value === selected)) {
    control.value = selected;
  }
}

function nodeOptions(nodes) {
  return nodes.map((node) => {
    const option = document.createElement("option");
    option.value = node.id;
    option.textContent = node.title || node.id;
    option.title = node.id;
    return option;
  });
}

function renderNodeSelect(id, nodes, multiple = false) {
  const control = byId(id);
  const selected = multiple
    ? new Set(Array.from(control.selectedOptions, (option) => option.value))
    : control.value;
  const options = nodeOptions(nodes);
  clear(control, options);
  if (multiple) {
    options.forEach((option) => {
      option.selected = selected.has(option.value);
    });
  } else if (options.some((option) => option.value === selected)) {
    control.value = selected;
  }
}

function localExportNodes(tree, projectId) {
  const nodes = tree?.nodes || [];
  const nodeById = new Map(nodes.map((node) => [node.id, node]));
  const children = new Map();
  nodes.forEach((node) => {
    if (!node.parent_id) return;
    const items = children.get(node.parent_id) || [];
    items.push(node.id);
    children.set(node.parent_id, items);
  });
  const levelOf = (node) => {
    let level = 1;
    let current = node;
    while (current.parent_id) {
      current = nodeById.get(current.parent_id);
      if (!current) return null;
      level += 1;
    }
    return level;
  };
  const project = nodeById.get(projectId);
  const projectLevel = project ? levelOf(project) : null;
  if (projectLevel === null) return [];
  const projectScope = new Set();
  const pending = [projectId];
  while (pending.length) {
    const nodeId = pending.shift();
    projectScope.add(nodeId);
    pending.push(...(children.get(nodeId) || []));
  }
  return nodes.filter((node) => (
    node.type !== "mounted" && (
      levelOf(node) === projectLevel || (
        projectScope.has(node.id)
        && (node.id === projectId || node.type !== "project")
      )
    )
  ));
}

function resultCard(row, index) {
  const card = text("article", "", "peer-result");
  card.append(
    text("b", row.library_node || "远程知识库"),
    text("small", [
      row.path || "未知路径",
      row.line_start && row.line_end
        ? `${row.line_start}-${row.line_end}` : "",
    ].filter(Boolean).join(" · ")),
    text("pre", row.content || "无可显示内容"),
  );
  const ready = row.mount_node && row.library_node && row.material_id;
  const control = button("查看完整材料", "secondary");
  control.dataset.peerAction = "material";
  control.dataset.resultIndex = String(index);
  control.disabled = !ready;
  if (!ready) control.title = "远程材料定位信息不完整";
  card.append(control);
  return card;
}

function renderResults(state) {
  const result = state.peerDispatch;
  if (!result) {
    clear(byId("peer-results"), [
      text("p", "尚未发起同级检索。", "empty-state"),
    ]);
    return;
  }
  const rows = Array.isArray(result.results) ? result.results : [];
  const header = text(
    "p",
    `${result.status || "UNKNOWN"} · ${rows.length} 条结果`,
    "muted",
  );
  clear(byId("peer-results"), [
    header,
    ...(rows.length ? rows.map(resultCard) : [
      text("p", "同级知识库未返回匹配结果。", "empty-state"),
    ]),
  ]);
}

export function renderPeerWorkspace(state) {
  const service = settings(state);
  renderService(state, service);
  renderPeers(state, service);
  renderResults(state);
  renderProjectSelect("peer-dispatch-project", state.snapshot);
  renderProjectSelect("peer-project-id", state.snapshot);
  const remoteLibraries = state.peerRemoteLibraries || [];
  renderNodeSelect("peer-remote-node-id", remoteLibraries);
  byId("peer-remote-node-id").disabled = remoteLibraries.length === 0;
  const projectId = byId("peer-project-id").value;
  renderNodeSelect(
    "peer-export-node-ids",
    localExportNodes(state.tree, projectId),
    true,
  );
  const status = state.peerError
    ? `局域网服务读取失败：${state.peerError}`
    : state.peerStatus;
  byId("peer-status").textContent = status;
}

export function showPeerMaterial(result) {
  const material = result?.material || {};
  byId("peer-material-title").textContent = material.path || "知识材料";
  byId("peer-material-meta").textContent = [
    result.remote_library_node || result.library_node,
    material.line_start && material.line_end
      ? `${material.line_start}-${material.line_end}` : "",
    material.scope,
  ].filter(Boolean).join(" · ");
  byId("peer-material-content").textContent = material.content
    || "无可显示内容";
  byId("peer-material-dialog").showModal();
}
