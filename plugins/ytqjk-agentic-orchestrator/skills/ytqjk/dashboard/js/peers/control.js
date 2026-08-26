import { api } from "../api.js";
import { byId } from "../ui/dom.js";
import { confirmAction } from "../ui/confirm.js";
import { renderPeerWorkspace, showPeerMaterial } from "./render.js";

export { renderPeerWorkspace };

let state;
let rerender = () => {};

function service() {
  return state.peer?.peer_service || null;
}

function revision() {
  const value = service()?.revision;
  if (!Number.isSafeInteger(value)) throw new Error("局域网配置尚未就绪");
  return value;
}

function accept(result, status = "") {
  state.peer = { ...(state.peer || {}), ...result };
  state.peerError = "";
  state.peerStatus = status;
  rerender();
}

async function run(control, action) {
  control.disabled = true;
  try {
    await action();
  } catch (error) {
    state.peerStatus = `操作失败：${error.message}`;
    rerender();
  } finally {
    control.disabled = false;
  }
}

function peer(peerId) {
  return service()?.peers?.find((item) => item.peer_id === peerId) || null;
}

function field(id, value) {
  const control = byId(id);
  if (control.type === "checkbox") control.checked = Boolean(value);
  else control.value = String(value ?? "");
}

function openPeer(record = null) {
  const form = byId("peer-form");
  const defaultProject = state.snapshot?.projects?.[0]?.id || "";
  form.reset();
  field("peer-id", record?.peer_id);
  field("peer-title", record?.title);
  field("peer-project-id", record?.project_id || defaultProject);
  field("peer-endpoint", record?.endpoint);
  field("peer-remote-node-id", record?.remote_node_id || defaultProject);
  field("peer-export-node-id", record?.export_node_id || defaultProject);
  field("peer-allow-insecure", record?.allow_insecure);
  field("peer-enabled", record ? record.enabled : true);
  field("peer-shared-secret", "");
  byId("peer-id").readOnly = Boolean(record);
  byId("peer-shared-secret").required = !record;
  byId("peer-dialog-title").textContent = record
    ? "编辑授权电脑" : "新增授权电脑";
  byId("peer-form-status").textContent = record
    ? "共享密钥留空会保留原值；已保存密钥不会回显。" : "";
  byId("peer-dialog").showModal();
}

function peerPayload() {
  const secret = byId("peer-shared-secret").value.trim();
  return {
    expected_revision: revision(),
    peer_id: byId("peer-id").value.trim(),
    title: byId("peer-title").value.trim(),
    project_id: byId("peer-project-id").value.trim(),
    endpoint: byId("peer-endpoint").value.trim(),
    secret: secret || null,
    remote_node_id: byId("peer-remote-node-id").value.trim(),
    export_node_id: byId("peer-export-node-id").value.trim(),
    allow_insecure: byId("peer-allow-insecure").checked,
    enabled: byId("peer-enabled").checked,
  };
}

async function savePeer(event) {
  event.preventDefault();
  const control = byId("save-peer");
  await run(control, async () => {
    const result = await api.peerUpsert(peerPayload());
    field("peer-shared-secret", "");
    byId("peer-dialog").close();
    accept(result, "授权电脑已保存");
  });
}

async function configure(event) {
  event.preventDefault();
  const control = byId("peer-service-save");
  await run(control, async () => {
    const enabled = byId("peer-service-enabled").checked;
    const allow = byId("peer-service-insecure").checked;
    const host = byId("peer-bind-host").value.trim();
    if (enabled && !["127.0.0.1", "::1"].includes(host) && !allow) {
      throw new Error("局域网监听需明确勾选未加密 HTTP 风险");
    }
    const result = await api.peerConfigure({
      expected_revision: revision(),
      enabled,
      bind_host: host,
      port: Number(byId("peer-port").value),
      allow_insecure_lan: allow,
    });
    byId("peer-service-form").dataset.dirty = "false";
    accept(result, "服务配置已保存；重启工作台后生效");
  });
}

async function bootstrap(control) {
  await run(control, async () => {
    const result = await api.peerBootstrap();
    accept(result, "局域网服务已初始化，默认仅监听本机");
  });
}

async function issueSecret(control) {
  await run(control, async () => {
    const result = await api.peerSecret();
    field("peer-local-id", result.local_peer_id);
    field("peer-secret-value", result.secret);
    byId("peer-secret-status").textContent = "密钥仅保留在当前对话框。";
    byId("peer-secret-dialog").showModal();
  });
}

async function removePeer(control, record) {
  const approved = await confirmAction(
    "删除授权电脑",
    `删除 ${record.title} 的连接配置；不会删除任何知识资料。`,
    "删除",
  );
  if (!approved) return;
  await run(control, async () => {
    const result = await api.peerRemove({
      expected_revision: revision(), peer_id: record.peer_id,
    });
    state.peerHealth.delete(record.peer_id);
    accept(result, "授权连接已删除");
  });
}

async function health(control, record) {
  await run(control, async () => {
    const result = await api.peerHealth({
      mount_id: record.peer_id, project_id: record.project_id,
    });
    state.peerHealth.set(
      record.peer_id,
      result.peer?.status || result.status || "READY",
    );
    state.peerStatus = `${record.title} 连接检查完成`;
    rerender();
  });
}

async function dispatch(event) {
  event.preventDefault();
  const control = byId("peer-dispatch-submit");
  await run(control, async () => {
    state.peerStatus = "正在向同级知识库检索";
    rerender();
    state.peerDispatch = await api.peerDispatch({
      project_id: byId("peer-dispatch-project").value.trim(),
      query: byId("peer-dispatch-query").value.trim(),
      limit: Number(byId("peer-dispatch-limit").value),
    });
    state.peerStatus = "同级检索已完成";
    rerender();
  });
}

async function material(control, index) {
  const dispatchResult = state.peerDispatch;
  const row = dispatchResult?.results?.[index];
  if (!row) return;
  await run(control, async () => {
    const result = await api.peerMaterial({
      project_id: dispatchResult.project_id,
      node_id: row.mount_node,
      remote_library_node: row.library_node,
      material_id: row.material_id,
    });
    showPeerMaterial(result);
  });
}

async function peerAction(event) {
  const control = event.target.closest("[data-peer-action]");
  if (!control) return;
  if (control.dataset.peerAction === "material") {
    await material(control, Number(control.dataset.resultIndex));
    return;
  }
  const record = peer(control.dataset.peerId);
  if (!record) return;
  if (control.dataset.peerAction === "edit") openPeer(record);
  if (control.dataset.peerAction === "remove") {
    await removePeer(control, record);
  }
  if (control.dataset.peerAction === "health") {
    await health(control, record);
  }
}

function closeDialog(id, clearSecret = false) {
  const dialog = byId(id);
  if (clearSecret) field("peer-secret-value", "");
  if (id === "peer-dialog") field("peer-shared-secret", "");
  dialog.close();
}

function bindDialogs() {
  byId("peer-form").onsubmit = savePeer;
  byId("close-peer-dialog").onclick = () => closeDialog("peer-dialog");
  byId("cancel-peer").onclick = () => closeDialog("peer-dialog");
  byId("close-peer-secret").onclick = () => closeDialog(
    "peer-secret-dialog", true,
  );
  byId("dismiss-peer-secret").onclick = () => closeDialog(
    "peer-secret-dialog", true,
  );
  byId("close-peer-material").onclick = () => closeDialog(
    "peer-material-dialog",
  );
  ["peer-dialog", "peer-secret-dialog", "peer-material-dialog"]
    .forEach((id) => {
      byId(id).onclick = (event) => {
        if (event.target !== event.currentTarget) return;
        closeDialog(id, id === "peer-secret-dialog");
      };
    });
  byId("peer-secret-dialog").addEventListener("close", () => {
    field("peer-secret-value", "");
    byId("peer-secret-status").textContent = "";
  });
  byId("peer-dialog").addEventListener("close", () => {
    field("peer-shared-secret", "");
  });
}

export function bindPeerWorkspace(dashboardState, onChange) {
  state = dashboardState;
  rerender = onChange;
  byId("peer-bootstrap").onclick = (event) => bootstrap(event.currentTarget);
  byId("peer-new").onclick = () => openPeer();
  byId("peer-secret").onclick = (event) => issueSecret(event.currentTarget);
  byId("peer-service-form").oninput = (event) => {
    event.currentTarget.dataset.dirty = "true";
  };
  byId("peer-service-form").onsubmit = configure;
  byId("peer-dispatch-form").onsubmit = dispatch;
  byId("peer-list").onclick = peerAction;
  byId("peer-results").onclick = peerAction;
  byId("copy-peer-secret").onclick = async () => {
    try {
      await navigator.clipboard.writeText(byId("peer-secret-value").value);
      byId("peer-secret-status").textContent = "已复制；请通过安全渠道发送。";
    } catch {
      byId("peer-secret-status").textContent = "无法自动复制，请手动选择。";
    }
  };
  bindDialogs();
}
