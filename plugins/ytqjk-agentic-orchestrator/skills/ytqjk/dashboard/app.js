const state = { data: null, selected: null, hasRenderedSnapshot: false };
const byId = (id) => document.getElementById(id);
const formatBytes = (value) => {
  const units = ["B", "KiB", "MiB", "GiB"];
  let amount = Number(value) || 0;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit += 1; }
  return `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(amount)} ${units[unit]}`;
};
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
  renderLibraries(); renderDocuments(); renderProjects(); renderSessions();
  state.hasRenderedSnapshot = true;
  document.body.classList.remove("is-loading");
}

function renderLibraries() {
  const library = state.data.global_library;
  const details = byId("global-library");
  details.replaceChildren(...[["位置", library.path], ["全局索引", formatTime(library.indexed_at)], ["知识索引", `${library.files} 文件 · ${library.chunks} 分块`], ["状态", `已验证 ${library.verified} · 已批准 ${library.approved} · 候选 ${library.candidate}`]].map(([term, value]) => {
    const row = document.createElement("div"); row.append(text("dt", term), text("dd", value)); return row;
  }));
  byId("project-library-count").textContent = `${state.data.projects.length} 个`;
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
    if (!state.hasRenderedSnapshot) card.classList.add("animate-in");
    card.style.setProperty("--item", index); const details = document.createElement("dl");
    [["状态", project.tracking], ["知识缓存", `${project.cache.entries} 分块 · ${formatBytes(project.cache.used_bytes)}`], ["源码索引", `${project.files} 文件 · ${project.chunks} 分块`], ["索引", formatTime(project.indexed_at)], ["向量", project.vector], ["Git", `${project.head} · ${project.dirty}`]].forEach(([term, value]) => {
      const row = document.createElement("div");
      row.append(text("dt", term), text("dd", value));
      details.append(row);
    });
    const open = text("button", "打开子库");
    open.className = "open-project-library"; open.onclick = () => showProjectLibrary(project);
    card.append(text("h3", project.name), text("p", project.id), details, open);
    return card;
  }));
}

function prepareLibraryDialog(kicker, title, status) {
  byId("library-kicker").textContent = kicker;
  byId("project-library-title").textContent = title;
  byId("project-library-meta").textContent = status;
  byId("project-library-empty").hidden = true;
  byId("project-library-files").replaceChildren();
  byId("project-library-dialog").showModal();
}

function appendIndexFiles(files, heading) {
  if (!files.length) return;
  const source = files.map((chunks) => {
    const item = document.createElement("details"); const summary = text("summary", `${chunks[0].path} · ${chunks.length} 分块`);
    const content = document.createElement("div"); content.className = "project-chunks";
    chunks.forEach((chunk) => { const part = document.createElement("article"); part.append(text("small", `第 ${chunk.line_start}-${chunk.line_end} 行`), text("pre", chunk.content)); content.append(part); });
    item.append(summary, content); return item;
  });
  byId("project-library-files").append(text("h3", heading), ...source);
}

async function showGlobalLibrary() {
  prepareLibraryDialog("总库", "总库知识索引", "读取总库索引...");
  let response;
  try { response = await fetch("/api/global-library"); }
  catch { byId("project-library-meta").textContent = "无法读取总库知识索引"; return; }
  if (!response.ok) { byId("project-library-meta").textContent = "无法读取总库知识索引"; return; }
  const library = await response.json();
  byId("project-library-meta").textContent = `知识索引 ${library.file_count}/${library.expected_files} 文件 · ${library.chunk_count}/${library.expected_chunks} 分块；索引于 ${formatTime(library.indexed_at)}`;
  byId("project-library-empty").textContent = "总库尚未建立可浏览的已验证或已批准知识索引。";
  byId("project-library-empty").hidden = library.files.length > 0;
  appendIndexFiles(library.files, "已验证与已批准知识");
}

async function showProjectLibrary(project) {
  prepareLibraryDialog("项目子库", project.name, "读取项目索引...");
  const response = await fetch("/api/project-library?id=" + encodeURIComponent(project.id));
  if (!response.ok) { byId("project-library-meta").textContent = "无法读取该项目子库"; return; }
  const library = await response.json();
  const cacheState = library.cache.capacity_exceeded ? " · 已超限" : "";
  byId("project-library-meta").textContent = `知识缓存 ${library.prefetch.length} 分块 · ${formatBytes(library.cache.used_bytes)}；源码索引 ${library.file_count}/${library.expected_files} 文件 · ${library.chunk_count}/${library.expected_chunks} 分块；子库占用 ${formatBytes(library.cache.project_used_bytes)}/${formatBytes(library.cache.capacity_bytes)} · ${library.cache.policy}${cacheState}`;
  byId("project-library-empty").textContent = "该项目尚未缓存知识，且尚未建立可浏览的源码索引。";
  byId("project-library-empty").hidden = library.files.length > 0 || library.prefetch.length > 0;
  const prefetch = library.prefetch.map((entry) => {
    const item = document.createElement("details"); item.className = "project-knowledge";
    const summary = text("summary", `总库预取 · 命中 ${entry.hit_count} 次 · ${entry.path} · 第 ${entry.line_start}-${entry.line_end} 行`);
    item.append(summary, text("pre", entry.content)); return item;
  });
  const content = byId("project-library-files");
  content.replaceChildren();
  if (prefetch.length) { content.append(text("h3", "总库预取缓存"), ...prefetch); }
  appendIndexFiles(library.files, "项目源码索引");
}

function renderSessions() {
  const groups = new Map();
  state.data.sessions.forEach((session) => {
    const group = groups.get(session.project) || [];
    group.push(session); groups.set(session.project, group);
  });
  byId("session-grid").replaceChildren(...[...groups.entries()].map(([project, sessions], index) => {
    const active = sessions.filter((session) => !session.archived_at);
    const saved = sessions.filter((session) => session.has_memory);
    const card = document.createElement("details");
    card.className = "session-group";
    if (!state.hasRenderedSnapshot) card.classList.add("animate-in");
    card.style.setProperty("--item", index);
    const summary = document.createElement("summary");
    const heading = document.createElement("div");
    heading.append(text("h3", project), text("p", `${active.length} 活动 · ${sessions.length - active.length} 已归档 · ${saved.length} 已保存记忆`));
    summary.append(heading, text("span", `${sessions.length} 个会话`));
    const list = document.createElement("div"); list.className = "session-list";
    sessions.forEach((session) => {
      const row = document.createElement("article");
      const status = session.archived_at ? "已归档" : "活动中";
      row.append(
        text("strong", `${status} · ${session.key}`),
        text("span", `最后活动 ${formatTime(session.last_activity_at)}`),
        text("span", session.has_memory ? "记忆已保存" : "未保存记忆"),
      );
      list.append(row);
    });
    card.append(summary, list);
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
  byId("approve-candidate").hidden = false;
  renderDocuments();
}

async function candidateRequest(method, payload, endpoint = "/api/candidate") {
  const response = await fetch(endpoint, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(payload) });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || "候选资料操作失败");
  return result;
}

async function submitIntake(name, content, encoding, purpose, relativePath = "", clearInputs = true, refresh = true) {
  setIntakeProgress("分析资料", 20);
  byId("intake-status").textContent = "保存中...";
  setIntakeProgress("拆分知识片段", 45);
  const response = await fetch("/api/intake", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name, content, encoding, purpose, relativePath }) });
  setIntakeProgress("评估批准条件", 75);
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || "保存失败");
  const assessment = payload.assessment;
  const stage = assessment.decision === "READY_FOR_REVIEW" ? "可人工复审" : "等待补充资料";
  const status = `资料已存为 CANDIDATE，已拆分 ${payload.chunks} 个知识片段：${assessment.reasons.join("；")}`;
  setIntakeProgress(stage, 100, true);
  byId("intake-status").textContent = status;
  rememberIntakeProgress(stage, 100, status);
  if (clearInputs) { byId("note").value = ""; byId("purpose").value = ""; byId("file-input").value = ""; }
  if (refresh) await loadSnapshot();
  return payload;
}

async function fileContent(file) {
  const content = await new Promise((resolve, reject) => { const reader = new FileReader(); reader.onload = () => resolve(String(reader.result).split(",")[1]); reader.onerror = () => reject(new Error("无法读取文件")); reader.readAsDataURL(file); });
  return content;
}

async function submitFile(file) {
  await submitIntake(file.name, await fileContent(file), "base64", byId("purpose").value);
}

async function submitFolder(files) {
  const total = files.length;
  let ready = 0; let needsWork = 0; let rejected = 0;
  const purpose = byId("purpose").value;
  for (const [index, file] of files.entries()) {
    setIntakeProgress(`导入 ${index + 1}/${total}：${file.name}`, Math.round((index / total) * 90));
    try {
      const result = await submitIntake(file.name, await fileContent(file), "base64", purpose, file.webkitRelativePath, false, false);
      if (result.assessment.decision === "READY_FOR_REVIEW") ready += 1; else needsWork += 1;
    } catch { rejected += 1; }
    setIntakeProgress(`可复审 ${ready} · 待补充 ${needsWork} · 拒绝 ${rejected}`, Math.round(((index + 1) / total) * 100), index + 1 === total);
  }
  const status = `文件夹已处理 ${total} 项：可复审 ${ready} 项，待补充 ${needsWork} 项，拒绝 ${rejected} 项`;
  byId("intake-status").textContent = status;
  rememberIntakeProgress(`可复审 ${ready} · 待补充 ${needsWork} · 拒绝 ${rejected}`, 100, status);
  byId("purpose").value = ""; byId("folder-input").value = "";
  await loadSnapshot();
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
byId("folder-input").onchange = async (event) => {
  const files = Array.from(event.target.files);
  if (!files.length) return;
  try { await submitFolder(files); } catch (error) { showIntakeError(error.message); }
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
byId("submit-intake").onclick = () => submitIntake("dashboard-note.md", byId("note").value, "utf8", byId("purpose").value).catch((error) => showIntakeError(error.message));
byId("save-candidate").onclick = async () => {
  if (!state.selected || state.selected.state !== "candidate") return;
  try { const result = await candidateRequest("PUT", { path: state.selected.path, content: byId("content").value }); byId("intake-status").textContent = `候选已保存：${result.assessment.reasons.join("；")}`; await loadSnapshot(); }
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
byId("open-global-library").onclick = showGlobalLibrary;
byId("close-project-library").onclick = () => byId("project-library-dialog").close();
byId("project-library-dialog").onclick = (event) => {
  if (event.target === event.currentTarget) event.currentTarget.close();
};
restoreIntakeProgress();
loadSnapshot().catch((error) => byId("updated").textContent = error.message);
setInterval(() => loadSnapshot().catch(() => undefined), 10000);
