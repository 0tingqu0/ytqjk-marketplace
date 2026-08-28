import { byId, button, clear, formatTime, text } from "../ui/dom.js";
import {
  renderKnowledgeGraphWorkbench,
} from "./knowledge-graph-workbench.js";

const STATUS_ITEMS = [
  ["已验证", "verified", "ph-shield-check"],
  ["已批准经验", "approved", "ph-seal-check"],
  ["候选经验", "candidate", "ph-clock-countdown"],
];

function icon(name, className = "") {
  const node = text("i", "", `ph ${name} ${className}`.trim());
  node.setAttribute("aria-hidden", "true");
  return node;
}

function metric(label, value, iconName, stateName) {
  const card = text("article", "", `metric metric-${stateName}`);
  const copy = text("span", "", "metric-copy");
  copy.append(text("span", label), text("strong", value));
  card.append(icon(iconName, "metric-icon"), copy);
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

function renderCandidates(documents, target, onReview) {
  const rows = documents
    .filter((item) => item.state === "candidate")
    .slice(0, 5);
  if (!rows.length) {
    const empty = text("div", "", "overview-empty");
    empty.append(
      icon("ph-file-magnifying-glass"),
      text("p", "暂无待审阅资料"),
    );
    clear(target, [empty]);
    return;
  }
  clear(target, rows.map((item) => {
    const row = button(item.label, "compact-row");
    row.append(text("span", item.path));
    row.onclick = () => onReview(item);
    return row;
  }));
}

export function renderOverview(snapshot, stale, onReview, graph, graphError) {
  const summary = byId("overview-summary");
  const candidates = byId("overview-candidates");
  if (!snapshot) {
    clear(summary, STATUS_ITEMS.map(([label, stateName, iconName]) =>
      metric(label, "–", iconName, stateName)));
    clear(byId("knowledge-topology"), [
      text("p", "正在读取知识图谱…", "topology-loading"),
    ]);
    clear(candidates, [text("p", "正在读取快照…", "muted")]);
    return;
  }
  const { counts, global_library: library, projects, documents } = snapshot;
  clear(summary, STATUS_ITEMS.map(([label, stateName, iconName]) =>
    metric(label, counts[stateName], iconName, stateName)));
  renderKnowledgeGraphWorkbench(
    byId("knowledge-topology"), snapshot, graph, graphError,
  );
  clear(byId("global-library"), [libraryRows(library)]);
  const globalState = snapshot.global.indexed_at ? "已建立" : "未建立";
  byId("project-library-summary").textContent =
    `${projects.length} 个项目子库；全局索引 ${globalState}。`;
  byId("overview-stale").hidden = !stale;
  renderCandidates(documents, candidates, onReview);
}
