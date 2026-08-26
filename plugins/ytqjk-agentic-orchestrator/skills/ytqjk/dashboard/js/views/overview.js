import { byId, button, clear, formatTime, text } from "../ui/dom.js";

function metric(label, value) {
  const card = text("article", "", "metric");
  card.append(text("span", label), text("strong", value));
  return card;
}

function libraryRows(library) {
  const rows = [
    ["位置", library.path],
    ["全局索引", formatTime(library.indexed_at)],
    ["知识索引", `${library.files} 文件 · ${library.chunks} 分块`],
  ];
  const list = document.createDocumentFragment();
  rows.forEach(([label, value]) => {
    list.append(text("dt", label), text("dd", value));
  });
  return list;
}

export function renderOverview(snapshot, stale, onReview) {
  const summary = byId("overview-summary");
  const candidates = byId("overview-candidates");
  if (!snapshot) {
    clear(summary, [
      metric("已验证", "读取中"),
      metric("已批准", "读取中"),
      metric("候选", "读取中"),
      metric("锚定会话", "读取中"),
    ]);
    clear(candidates, [text("p", "正在读取快照…", "muted")]);
    return;
  }
  const { counts, global_library: library, projects, documents } = snapshot;
  clear(summary, [
    metric("已验证", counts.verified),
    metric("已批准经验", counts.approved),
    metric("候选经验", counts.candidate),
    metric("锚定会话", counts.sessions),
  ]);
  clear(byId("global-library"), [libraryRows(library)]);
  const globalState = snapshot.global.indexed_at ? "已建立" : "未建立";
  byId("project-library-summary").textContent =
    `${projects.length} 个项目子库；全局索引 ${globalState}。`;
  byId("overview-stale").hidden = !stale;
  const rows = documents
    .filter((item) => item.state === "candidate")
    .slice(0, 5);
  if (!rows.length) {
    clear(candidates, [text("p", "没有等待人工审阅的候选资料。", "muted")]);
    return;
  }
  clear(candidates, rows.map((item) => {
    const row = button(item.label, "compact-row");
    row.append(text("span", item.path));
    row.onclick = () => onReview(item);
    return row;
  }));
}
