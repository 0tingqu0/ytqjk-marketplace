import { byId, clear, formatTime, text } from "../ui/dom.js";

export function renderSessions(snapshot) {
  const target = byId("session-grid");
  if (!snapshot) { clear(target, [text("p", "正在读取会话锚点…", "muted")]); return; }
  if (!snapshot.sessions.length) {
    clear(target, [
      text("p", "尚无匿名会话锚点。", "empty-state"),
    ]);
    return;
  }
  const groups = new Map();
  snapshot.sessions.forEach((session) => {
    const current = groups.get(session.project) || [];
    groups.set(session.project, [...current, session]);
  });
  clear(target, [...groups.entries()].map(([project, sessions]) => {
    const card = document.createElement("details");
    card.className = "session-group";
    const active = sessions.filter((item) => !item.archived_at).length;
    const archived = sessions.length - active;
    const memories = sessions.filter((item) => item.has_memory).length;
    const heading = text("summary", "");
    heading.append(
      text("h3", project),
      text(
        "p",
        `${active} 活动 · ${archived} 已归档 · ${memories} 已保存记忆`,
      ),
    );
    const list = text("div", "", "session-list");
    sessions.forEach((session) => {
      const row = document.createElement("article");
      const status = session.archived_at ? "已归档" : "活动中";
      const memory = session.has_memory ? "记忆已保存" : "未保存记忆";
      row.append(
        text("strong", `${status} · ${session.key}`),
        text("span", `最后活动 ${formatTime(session.last_activity_at)}`),
        text("span", memory),
      );
      list.append(row);
    });
    card.append(heading, list);
    return card;
  }));
}
