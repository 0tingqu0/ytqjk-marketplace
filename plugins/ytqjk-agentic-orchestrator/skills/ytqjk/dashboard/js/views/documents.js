import { selectionFor } from "../draft-conflicts.js";
import { confirmAction } from "../ui/confirm.js";
import { byId, button, clear, icon, stateLabel, text } from "../ui/dom.js";

export function createDocumentActions(api, state, callbacks) {
  const { deleteDraft, refresh, render } = callbacks;
  async function selectItem(item, key) {
    const selected = state[key];
    if (
      selected?.item.path === item.path
      && state.drafts.has(item.path)
    ) return;
    state[key] = { item, loading: true, content: "", error: "" };
    render();
    try {
      const payload = await api.document(item.path);
      if (state[key]?.item.path === item.path) {
        state[key] = {
          item,
          content: payload.content,
          version: payload.version,
          conflict: "",
          loading: false,
          error: "",
        };
      }
    } catch (error) {
      if (state[key]?.item.path === item.path) {
        state[key] = {
          item,
          loading: false,
          content: "",
          error: error.message,
        };
      }
    }
    render();
  }
  async function saveCandidate(item, content) {
    try {
      const selection = selectionFor(state, item);
      const result = await api.candidate("PUT", {
        path: item.path,
        content,
        expected_version: selection?.version,
      });
      if (selection) {
        selection.content = content;
        selection.version = result.version;
        selection.conflict = "";
      }
      deleteDraft(item);
      const reasons = (result.assessment?.reasons || []).join("；");
      byId("updated").textContent =
        `候选已保存：${reasons || "等待人工复审"}`;
      await refresh();
    } catch (error) {
      byId("updated").textContent = error.message;
    }
  }
  async function approveCandidate(item) {
    const confirmed = await confirmAction(
      "批准候选资料",
      "将候选资料移入已批准经验；这不代表来源已验证。",
      "批准",
    );
    if (!confirmed) return;
    try {
      await api.candidate(
        "POST",
        { path: item.path },
        "/api/candidate/approve",
      );
      deleteDraft(item);
      state.selected = null;
      state.reviewSelected = null;
      byId("updated").textContent = "候选资料已批准入库";
      await refresh();
    } catch (error) {
      byId("updated").textContent = error.message;
    }
  }
  async function deleteCandidate(item) {
    const confirmed = await confirmAction(
      "删除候选资料",
      "将删除该候选资料及其关联原件和知识片段。",
      "删除",
    );
    if (!confirmed) return;
    try {
      await api.candidate("DELETE", { path: item.path });
      deleteDraft(item);
      state.selected = null;
      state.reviewSelected = null;
      byId("updated").textContent = "候选资料已删除";
      await refresh();
    } catch (error) {
      byId("updated").textContent = error.message;
    }
  }
  return {
    selectDocument: (item) => selectItem(item, "selected"),
    selectReview: (item) => selectItem(item, "reviewSelected"),
    saveCandidate,
    approveCandidate,
    deleteCandidate,
  };
}

function filteredDocuments(snapshot, state) {
  const query = state.documentFilter.trim().toLowerCase();
  return snapshot.documents.filter((item) => {
    const matchesState =
      state.documentState === "all"
      || item.state === state.documentState;
    const searchable = [item.path, item.label, item.state]
      .join(" ")
      .toLowerCase();
    return matchesState && (!query || searchable.includes(query));
  });
}

function renderPreview(selected, state) {
  const empty = byId("document-empty");
  const preview = byId("preview");
  if (!selected) { empty.hidden = false; preview.hidden = true; return; }
  empty.hidden = true;
  preview.hidden = false;
  byId("path").textContent = selected.item.path;
  const tag = byId("state");
  tag.textContent = stateLabel(selected.item);
  tag.className = `state-tag ${selected.item.state}`;
  const content = byId("content");
  content.hidden = false;
  content.readOnly = selected.item.state !== "candidate";
  const saved = state.drafts.get(selected.item.path);
  content.value = selected.loading
    ? "读取中…"
    : selected.error || saved || selected.content || "";
  content.setAttribute("aria-invalid", String(Boolean(selected.conflict)));
  content.title = selected.conflict || "";
  content.oninput = selected.item.state === "candidate" ? () => {
    state.drafts.set(selected.item.path, content.value);
  } : null;
  byId("preview-actions").hidden = selected.item.state !== "candidate";
}

function documentListEmpty(filtered) {
  const empty = text("div", "", "list-empty-state");
  empty.append(
    icon(filtered ? "ph-magnifying-glass" : "ph-files"),
    text("h3", filtered ? "没有匹配结果" : "知识库还是空的"),
    text(
      "p",
      filtered ? "调整关键词或状态筛选后再试。" : "投递资料后会在这里形成可检索文档。",
    ),
  );
  return empty;
}

export function renderDocuments(snapshot, state, onSelect) {
  const target = byId("documents");
  if (!snapshot) { clear(target, [text("p", "正在读取文档…", "muted")]); return; }
  const rows = filteredDocuments(snapshot, state);
  if (!rows.length) {
    const filtered = Boolean(
      state.documentFilter.trim() || state.documentState !== "all",
    );
    clear(target, [documentListEmpty(filtered)]);
  }
  else clear(target, rows.map((item) => {
    const row = button("", "document-row");
    row.setAttribute(
      "aria-current",
      String(state.selected?.item.path === item.path),
    );
    const meta = text("span", "");
    meta.append(text("b", item.label), text("small", item.path));
    const tag = text("span", stateLabel(item), `state-tag ${item.state}`);
    row.append(tag, meta);
    row.onclick = () => onSelect(item);
    return row;
  }));
  renderPreview(state.selected, state);
}
