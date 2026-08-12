const state = { data: null, selected: null };
const byId = (id) => document.getElementById(id);
const formatBytes = (value) => new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(value) + " B";
const formatTime = (value) => value ? new Date(value).toLocaleString("zh-CN", { hour12: false }) : "未索引";
const text = (tag, value) => { const node = document.createElement(tag); node.textContent = value; return node; };

async function loadSnapshot() {
  byId("updated").textContent = "刷新中";
  const response = await fetch("/api/snapshot", { cache: "no-store" });
  if (!response.ok) throw new Error("无法读取知识库");
  state.data = await response.json();
  byId("verified-count").textContent = state.data.counts.verified;
  byId("approved-count").textContent = state.data.counts.approved;
  byId("candidate-count").textContent = state.data.counts.candidate;
  byId("session-count").textContent = state.data.counts.sessions;
  byId("root").textContent = state.data.root;
  byId("updated").textContent = "已刷新 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
  byId("global-status").textContent = `全局索引 ${state.data.global.indexed_at ? formatTime(state.data.global.indexed_at) : "未建立"}`;
  renderDocuments(); renderProjects(); renderSessions();
}

function renderDocuments() {
  const filter = byId("filter").value.trim().toLowerCase();
  const rows = state.data.documents.filter((item) => [item.path, item.label, item.state].join(" ").toLowerCase().includes(filter));
  byId("documents").replaceChildren(...rows.map((item) => {
    const button = document.createElement("button");
    button.className = `document ${item.state}`;
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
  byId("project-grid").replaceChildren(...state.data.projects.map((project) => {
    const card = document.createElement("article");
    const details = document.createElement("dl");
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
  byId("session-grid").replaceChildren(...state.data.sessions.map((session) => {
    const card = document.createElement("article");
    const details = document.createElement("dl");
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
}

async function candidateRequest(method, payload) {
  const response = await fetch("/api/candidate", { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || "候选资料操作失败");
  return result;
}

async function submitIntake(name, content, encoding) {
  byId("intake-status").textContent = "保存中...";
  const response = await fetch("/api/intake", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, content, encoding }) });
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || "保存失败");
  const assessment = payload.assessment;
  const result = assessment.decision === "READY_FOR_REVIEW" ? "可提交批准审阅" : assessment.reasons.join("；");
  byId("intake-status").textContent = `已保存为 CANDIDATE，已拆分 ${payload.chunks} 个知识片段：${result}`;
  byId("note").value = ""; byId("file-input").value = "";
  await loadSnapshot();
}

async function submitFile(file) {
  const content = await new Promise((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result).split(",")[1]); reader.onerror = () => reject(new Error("无法读取文件")); reader.readAsDataURL(file); });
  await submitIntake(file.name, content, "base64");
}

byId("file-input").onchange = async (event) => {
  const file = event.target.files[0];
  if (!file) return;
  try { await submitFile(file); } catch (error) { byId("intake-status").textContent = error.message; }
};
const dropZone = document.querySelector(".drop-zone");
dropZone.ondragover = (event) => { event.preventDefault(); dropZone.classList.add("dragging"); };
dropZone.ondragleave = () => dropZone.classList.remove("dragging");
dropZone.ondrop = async (event) => {
  event.preventDefault(); dropZone.classList.remove("dragging");
  const file = event.dataTransfer.files[0];
  if (!file) return;
  try { await submitFile(file); } catch (error) { byId("intake-status").textContent = error.message; }
};
byId("submit-intake").onclick = () => submitIntake("dashboard-note.md", byId("note").value, "utf8").catch((error) => byId("intake-status").textContent = error.message);
byId("save-candidate").onclick = async () => {
  if (!state.selected || state.selected.state !== "candidate") return;
  try { const result = await candidateRequest("PUT", { path: state.selected.path, content: byId("content").value }); const assessment = result.assessment; byId("intake-status").textContent = assessment.decision === "READY_FOR_REVIEW" ? "候选已保存：可提交批准审阅" : `候选已保存：${assessment.reasons.join("；")}`; await loadSnapshot(); }
  catch (error) { byId("intake-status").textContent = error.message; }
};
byId("delete-candidate").onclick = async () => {
  if (!state.selected || state.selected.state !== "candidate" || !confirm("删除该候选资料及其关联原件？")) return;
  try { await candidateRequest("DELETE", { path: state.selected.path }); byId("preview").hidden = true; byId("empty").hidden = false; state.selected = null; byId("intake-status").textContent = "候选资料已删除"; await loadSnapshot(); }
  catch (error) { byId("intake-status").textContent = error.message; }
};

byId("refresh").onclick = () => loadSnapshot().catch((error) => byId("updated").textContent = error.message);
byId("filter").oninput = renderDocuments;
loadSnapshot().catch((error) => byId("updated").textContent = error.message);
setInterval(() => loadSnapshot().catch(() => undefined), 10000);
