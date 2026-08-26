import { api } from "../api.js";
import { byId } from "../ui/dom.js";

let state;
let accept;
let rerender;
let revision;
let run;

function field(id, value) {
  const control = byId(id);
  if (control.type === "checkbox") control.checked = Boolean(value);
  else control.value = String(value ?? "");
}

function selectValues(id, values) {
  const selected = new Set(values || []);
  Array.from(byId(id).options).forEach((option) => {
    option.selected = selected.has(option.value);
  });
}

function selectedValues(id) {
  return Array.from(
    byId(id).selectedOptions,
    (option) => option.value,
  );
}

export function openPeer(record = null) {
  const form = byId("peer-form");
  const defaultProject = state.snapshot?.projects?.[0]?.id || "";
  form.reset();
  field("peer-project-id", record?.project_id || defaultProject);
  state.peerRemoteLibraries = record?.remote_node_id ? [{
    id: record.remote_node_id,
    title: record.remote_node_id,
    type: "project",
  }] : [];
  rerender();
  field("peer-id", record?.peer_id);
  field("peer-title", record?.title);
  field("peer-project-id", record?.project_id || defaultProject);
  field("peer-endpoint", record?.endpoint);
  field("peer-remote-node-id", record?.remote_node_id);
  selectValues(
    "peer-export-node-ids",
    record?.export_node_ids || [record?.export_node_id || defaultProject],
  );
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
    remote_node_id: byId("peer-remote-node-id").value.trim() || null,
    export_node_ids: selectedValues("peer-export-node-ids"),
    allow_insecure: byId("peer-allow-insecure").checked,
    enabled: byId("peer-enabled").checked,
  };
}

function discoveryPayload() {
  const secret = byId("peer-shared-secret").value.trim();
  return {
    peer_id: byId("peer-id").value.trim(),
    project_id: byId("peer-project-id").value.trim(),
    endpoint: byId("peer-endpoint").value.trim(),
    secret: secret || null,
    allow_insecure: byId("peer-allow-insecure").checked,
  };
}

async function discover(control) {
  const required = ["peer-id", "peer-project-id", "peer-endpoint"];
  const secret = byId("peer-shared-secret");
  if (required.some((id) => !byId(id).reportValidity())
      || !secret.reportValidity()) return;
  await run(control, async () => {
    const previous = byId("peer-remote-node-id").value;
    const result = await api.peerDiscover(discoveryPayload());
    state.peerRemoteLibraries = result.peer.export_nodes;
    rerender();
    const available = state.peerRemoteLibraries.some(
      (item) => item.id === previous,
    );
    field(
      "peer-remote-node-id",
      available ? previous : state.peerRemoteLibraries[0]?.id,
    );
    byId("peer-form-status").textContent = (
      `已获取 ${state.peerRemoteLibraries.length} 个开放库，请选择访问目标。`
    );
  });
}

function invalidateDiscovery(resetExports = false) {
  state.peerRemoteLibraries = [];
  rerender();
  byId("peer-form-status").textContent = "连接信息已变化，请重新获取开放库。";
  if (resetExports) {
    selectValues("peer-export-node-ids", [byId("peer-project-id").value]);
  }
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

export function bindPeerForm(dashboardState, callbacks) {
  state = dashboardState;
  ({ accept, rerender, revision, run } = callbacks);
  byId("peer-form").onsubmit = savePeer;
  byId("peer-discover").onclick = (event) => discover(event.currentTarget);
  byId("peer-project-id").onchange = () => invalidateDiscovery(true);
  ["peer-id", "peer-endpoint", "peer-shared-secret", "peer-allow-insecure"]
    .forEach((id) => {
      byId(id).onchange = () => invalidateDiscovery();
    });
}
