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
  byId("root").textContent = state.data.root;
  byId("updated").textContent = "已刷新 " + new Date().toLocaleTimeString("zh-CN", { hour12: false });
  byId("global-status").textContent = `全局索引 ${state.data.global.indexed_at ? formatTime(state.data.global.indexed_at) : "未建立"}`;
  renderDocuments(); renderProjects();
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

async function showDocument(item) {
  byId("empty").hidden = true; byId("preview").hidden = false;
  byId("path").textContent = item.path; byId("state").textContent = item.state === "candidate" ? "CANDIDATE" : item.label;
  byId("state").className = item.state;
  byId("content").textContent = "读取中...";
  const response = await fetch("/api/document?path=" + encodeURIComponent(item.path));
  const payload = await response.json();
  byId("content").textContent = payload.content;
}

async function submitIntake(name, content) {
  byId("intake-status").textContent = "保存中...";
  const response = await fetch("/api/intake", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, content }) });
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || "保存失败");
  byId("intake-status").textContent = `已保存为 CANDIDATE：${payload.path}`;
  byId("note").value = ""; byId("file-input").value = "";
  await loadSnapshot();
}

byId("file-input").onchange = async (event) => {
  const file = event.target.files[0];
  if (!file) return;
  try { await submitIntake(file.name, await file.text()); } catch (error) { byId("intake-status").textContent = error.message; }
};
const dropZone = document.querySelector(".drop-zone");
dropZone.ondragover = (event) => { event.preventDefault(); dropZone.classList.add("dragging"); };
dropZone.ondragleave = () => dropZone.classList.remove("dragging");
dropZone.ondrop = async (event) => {
  event.preventDefault(); dropZone.classList.remove("dragging");
  const file = event.dataTransfer.files[0];
  if (!file) return;
  try { await submitIntake(file.name, await file.text()); } catch (error) { byId("intake-status").textContent = error.message; }
};
byId("submit-intake").onclick = () => submitIntake("dashboard-note.md", byId("note").value).catch((error) => byId("intake-status").textContent = error.message);

byId("refresh").onclick = () => loadSnapshot().catch((error) => byId("updated").textContent = error.message);
byId("filter").oninput = renderDocuments;
loadSnapshot().catch((error) => byId("updated").textContent = error.message);
