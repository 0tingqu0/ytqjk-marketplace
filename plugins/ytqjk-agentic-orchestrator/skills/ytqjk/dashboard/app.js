const state = { data: null, selected: null };
const byId = (id) => document.getElementById(id);
const formatBytes = (value) => new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(value) + " B";
const formatTime = (value) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "未索引";
const text = (tag, value) => { const node = document.createElement(tag); node.textContent = value; return node; };
const intakeProgressKey = "ytqjk-last-intake-progress";

function setIntakeProgress(stage, percent, complete = false) {
  byId("intake-progress").hidden = false;
  byId("intake-stage").textContent = stage;
  byId("intake-percent").textContent = `${percent}%`;
  const bar = byId("intake-progress-bar");
  bar.style.width = `${percent}%`;
  bar.style.backgroundColor = complete ? "#29ae9b" : "#167cba";
}

function rememberIntakeProgress(stage, percent, status) {
  localStorage.setItem(intakeProgressKey, JSON.stringify({ stage, percent, status }));
}

function restoreIntakeProgress() {
  try {
    const saved = JSON.parse(localStorage.getItem(intakeProgressKey) || "null");
    if (!saved || typeof saved.stage !== "string" || typeof saved.percent !== "number") return;
    setIntakeProgress(saved.stage, saved.percent, true);
    byId("intake-status").textContent = typeof saved.status === "string" ? saved.status : "";
  } catch { localStorage.removeItem(intakeProgressKey); }
}

async function loadSnapshot() {
  document.body.classList.add("is-loading");
  byId("updated").textContent = "刷新中";
  const response = await fetch("/api/snapshot", { cache: "no-store" });
  if (!response.ok) throw new Error("无法读取知识库");
  state.data = await response.json();
  [["verified-count", state.data.counts.verified], ["approved-count", state.data.counts.approved], ["candidate-count", state.data.counts.candidate], ["session-count", state.data.counts.sessions]].forEach(([id, value]) => {
    const node = byId(id); if (node.textContent !== String(value)) { node.classList.remove("count-flash"); void node.offsetWidth; node.classList.add("count-flash"); } node.textContent = value;
  });
  byId("root").textContent = state.data.root;
  byId("updated").textContent = "已刷新 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
  byId("global-status").textContent = `全局索引 ${state.data.global.indexed_at ? formatTime(state.data.global.indexed_at) : "未建立"}`;
  renderDocuments(); renderProjects(); renderSessions(); document.body.classList.remove("is-loading");
}

function renderDocuments() {
  const filter = byId("filter").value.trim().toLowerCase();
  const rows = state.data.documents.filter((item) => [item.path, item.label, item.state].join(" ").toLowerCase().includes(filter));
  byId("documents").replaceChildren(...rows.map((item) => {
    const button = document.createElement("button");
    button.className = `document ${item.state}${state.selected?.path === item.path ? " selected" : ""}`;
    const detail = document.createElement("span");
    detail.append(text("b", item.label), text("small", item.path));
    const dot = document.createElement("span");
    dot.className = `dot ${item.state}`;
    button.append(dot, detail);
    button.onclick = () => showDocument(item);
    return button;
  }));
}

function renderProjects() {
  byId("project-grid").replaceChildren(...state.data.projects.map((project, index) => {
    const card = document.createElement("article");
    card.style.setProperty("--item", index); const details = document.createElement("dl");
    [["索引", formatTime(project.indexed_at)], ["内容", `${project.files} 文件 · ${project.chunks} 分块`], ["向量", project.vector], ["Git", `${project.head} · ${project.dirty}`]].forEach(([term, value]) => {
      const row = document.createElement("div");
      row.append(text("dt", term), text("dd", value));
      details.append(row);
    });
    card.append(text("h3", project.name), text("p", project.id), details);
    return card;
  }));
}

function renderSessions() {
  byId("session-grid").replaceChildren(...state.data.sessions.map((session, index) => {
    const card = document.createElement("article");
    card.style.setProperty("--item", index); const details = document.createElement("dl");
    const status = session.archived_at ? "已归档" : "活动中";
    [["匿名锚点", session.key], ["项目", session.project], ["最后活动", formatTime(session.last_activity_at)], ["记忆", session.has_memory ? "已保存" : "未保存"], ["状态", status]].forEach(([term, value]) => {
      const row = document.createElement("div");
      row.append(text("dt", term), text("dd", value));
      details.append(row);
    });
    card.append(text("h3", status), details);
    return card;
  }));
}

async function showDocument(item) {
  byId("empty").hidden = true; byId("preview").hidden = false;
  byId("path").textContent = item.path; byId("state").textContent = item.state === "candidate" ? "CANDIDATE" : item.label;
  byId("state").className = item.state;
  byId("content").hidden = false; byId("content").readOnly = item.state !== "candidate"; byId("content").value = "读取中...";
  byId("preview-actions").hidden = item.state !== "candidate";
  const response = await fetch("/api/document?path=" + encodeURIComponent(item.path));
  const payload = await response.json();
  byId("content").value = payload.content;
  state.selected = item;
  byId("approve-candidate").hidden = isReadyForApproval(payload.content);
  renderDocuments();
}

function isReadyForApproval(content) {
  return content.includes("结论：`READY_FOR_REVIEW`");
}

async function candidateRequest(method, payload, endpoint = "/api/candidate") {
  const response = await fetch(endpoint, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || "候选资料操作失败");
  return result;
}

async function submitIntake(name, content, encoding) {
  setIntakeProgress("分析资料", 20);
  byId("intake-status").textContent = "保存中...";
  setIntakeProgress("拆分知识片段", 45);
  const response = await fetch("/api/intake", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, content, encoding }) });
  setIntakeProgress("评估批准条件", 75);
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || "保存失败");
  const assessment = payload.assessment;
  const result = payload.state === "approved" ? "已自动批准" : assessment.reasons.join("；");
  const stage = payload.state === "approved" ? "已自动批准" : "等待人工批准";
  const status = `资料已${payload.state === "approved" ? "自动批准" : "存为 CANDIDATE"}，已拆分 ${payload.chunks} 个知识片段：${result}`;
  setIntakeProgress(stage, 100, true);
  byId("intake-status").textContent = status;
  rememberIntakeProgress(stage, 100, status);
  byId("note").value = ""; byId("file-input").value = "";
  await loadSnapshot();
}

async function submitFile(file) {
  const content = await new Promise((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result).split(",")[1]); reader.onerror = () => reject(new Error("无法读取文件")); reader.readAsDataURL(file); });
  await submitIntake(file.name, content, "base64");
}

function showIntakeError(message) {
  setIntakeProgress("处理失败", 100, true);
  byId("intake-progress-bar").style.backgroundColor = "#e8624d";
  byId("intake-status").textContent = message;
  rememberIntakeProgress("处理失败", 100, message);
}

byId("file-input").onchange = async (event) => {
  const file = event.target.files[0];
  if (!file) return;
  try { await submitFile(file); } catch (error) { showIntakeError(error.message); }
};
const dropZone = document.querySelector(".drop-zone");
dropZone.ondragover = (event) => { event.preventDefault(); dropZone.classList.add("dragging"); };
dropZone.ondragleave = () => dropZone.classList.remove("dragging");
dropZone.ondrop = async (event) => {
  event.preventDefault(); dropZone.classList.remove("dragging");
  const file = event.dataTransfer.files[0];
  if (!file) return;
  try { await submitFile(file); } catch (error) { showIntakeError(error.message); }
};
byId("submit-intake").onclick = () => submitIntake("dashboard-note.md", byId("note").value, "utf8").catch((error) => showIntakeError(error.message));
byId("save-candidate").onclick = async () => {
  if (!state.selected || state.selected.state !== "candidate") return;
  try { const result = await candidateRequest("PUT", { path: state.selected.path, content: byId("content").value }); byId("approve-candidate").hidden = result.state === "approved" || result.assessment.decision === "READY_FOR_REVIEW"; byId("intake-status").textContent = result.state === "approved" ? "候选已自动批准" : `候选已保存：${result.assessment.reasons.join("；")}`; await loadSnapshot(); }
  catch (error) { byId("intake-status").textContent = error.message; }
};
byId("approve-candidate").onclick = async () => {
  if (!state.selected || state.selected.state !== "candidate" || !confirm("确认批准此候选资料入库？")) return;
  try {
    await candidateRequest("POST", { path: state.selected.path }, "/api/candidate/approve");
    byId("preview").hidden = true; byId("empty").hidden = false; state.selected = null;
    byId("intake-status").textContent = "候选资料已批准入库"; await loadSnapshot();
  } catch (error) { byId("intake-status").textContent = error.message; }
};
byId("delete-candidate").onclick = async () => {
  if (!state.selected || state.selected.state !== "candidate" || !confirm("删除该候选资料及其关联原件？")) return;
  try { await candidateRequest("DELETE", { path: state.selected.path }); byId("preview").hidden = true; byId("empty").hidden = false; state.selected = null; byId("intake-status").textContent = "候选资料已删除"; await loadSnapshot(); }
  catch (error) { byId("intake-status").textContent = error.message; }
};

byId("refresh").onclick = () => loadSnapshot().catch((error) => byId("updated").textContent = error.message);
byId("filter").oninput = renderDocuments;
restoreIntakeProgress();
loadSnapshot().catch((error) => byId("updated").textContent = error.message);
setInterval(() => loadSnapshot().catch(() => undefined), 10000);
