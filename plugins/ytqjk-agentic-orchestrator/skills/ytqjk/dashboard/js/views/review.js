import { byId, button, clear, icon, text } from "../ui/dom.js";

function reviewEditor(selected, state, actions) {
  const target = byId("review-detail");
  const item = selected.item;
  const heading = text("div", "");
  heading.append(
    text("span", "CANDIDATE", "state-tag candidate"),
    text("p", item.path, "muted"),
  );
  if (selected.loading || selected.error) {
    const message = selected.loading ? "读取中…" : selected.error;
    clear(target, [
      heading,
      text("p", message, "status-message"),
    ]);
    return;
  }
  const editor = document.createElement("textarea");
  editor.id = "review-content";
  editor.rows = 18;
  editor.value = state.drafts.get(item.path) ?? selected.content ?? "";
  editor.oninput = () => state.drafts.set(item.path, editor.value);
  const save = button("保存修改", "secondary icon-leading");
  const approve = button("批准入库", "icon-leading");
  const remove = button("删除候选", "danger icon-leading");
  save.prepend(icon("ph-floppy-disk"));
  approve.prepend(icon("ph-seal-check"));
  remove.prepend(icon("ph-trash"));
  save.onclick = () => actions.save(item, editor.value);
  approve.onclick = () => actions.approve(item);
  remove.onclick = () => actions.remove(item);
  const controls = text("div", "", "form-actions");
  controls.append(save, approve, remove);
  const rows = [heading];
  if (selected.conflict) {
    rows.push(text("p", selected.conflict, "status-message"));
  }
  clear(target, [...rows, editor, controls]);
}

export function renderReview(snapshot, state, actions) {
  const list = byId("review-list");
  const empty = byId("review-empty");
  const detail = byId("review-detail");
  const candidates = snapshot
    ? snapshot.documents.filter((item) => item.state === "candidate")
    : [];
  if (!snapshot) clear(list, [text("p", "正在读取候选资料…", "muted")]);
  else if (!candidates.length) clear(list, [text("p", "没有候选资料等待审阅。", "muted")]);
  else clear(list, candidates.map((item) => {
    const row = button("", "document-row");
    row.setAttribute(
      "aria-current",
      String(state.reviewSelected?.item.path === item.path),
    );
    const meta = text("span", "");
    meta.append(text("b", item.label), text("small", item.path));
    row.append(text("span", "CANDIDATE", "state-tag candidate"), meta);
    row.onclick = () => actions.select(item);
    return row;
  }));
  empty.hidden = Boolean(state.reviewSelected);
  detail.hidden = !state.reviewSelected;
  if (state.reviewSelected) reviewEditor(state.reviewSelected, state, actions);
}
