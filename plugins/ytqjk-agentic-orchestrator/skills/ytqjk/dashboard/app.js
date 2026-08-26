import { api } from "./js/api.js";
import { restoreIntakeResults, restoreTheme } from "./js/store.js";
import { saveTheme, state } from "./js/store.js";
import { ROUTES, createRouter } from "./js/router.js";
import { byId } from "./js/ui/dom.js";
import { bindLibraryDialog } from "./js/ui/library-dialog.js";
import { showGlobalLibrary } from "./js/ui/library-dialog.js";
import { showProjectLibrary } from "./js/ui/library-dialog.js";
import { bindTreeDialog, openTreeAction } from "./js/ui/tree-dialog.js";
import { bindCommandPalette } from "./js/command-palette.js";
import { detectDraftConflicts } from "./js/draft-conflicts.js";
import { renderOverview } from "./js/views/overview.js";
import { createDocumentActions } from "./js/views/documents.js";
import { renderDocuments } from "./js/views/documents.js";
import { renderReview } from "./js/views/review.js";
import { renderLibraries } from "./js/views/libraries.js";
import { renderSessions } from "./js/views/sessions.js";
import { bindIntake } from "./js/views/intake.js";
import { bindPeerWorkspace, renderPeerWorkspace } from "./js/peers/control.js";
const THEME_LABELS = {
  system: "系统", light: "浅色", dark: "暗色",
};
const NEXT_THEME = {
  system: "light", light: "dark", dark: "system",
};
let router, documentActions;
function setTheme(theme) {
  document.body.dataset.theme = theme;
  byId("theme-toggle").textContent = `主题：${THEME_LABELS[theme]}`;
  saveTheme(theme);
}
function showRoute(route, focus = false) {
  state.route = route;
  const [kicker, title] = ROUTES[route];
  byId("view-kicker").textContent = kicker;
  byId("view-title").textContent = title;
  document.querySelectorAll(".view-panel").forEach((panel) => {
    panel.hidden = panel.dataset.view !== route;
  });
  document.querySelectorAll("[data-route]").forEach((node) => {
    const current = node.dataset.route === route ? "page" : "false";
    node.setAttribute("aria-current", String(current));
  });
  document.querySelector(".app-shell").classList.remove("rail-open");
  byId("rail-toggle").setAttribute("aria-expanded", "false");
  if (focus) byId("view-root").focus();
}
function renderAll() {
  const snapshot = state.snapshot;
  renderOverview(snapshot, state.stale, (item) => {
    documentActions.selectReview(item);
    router.go("review");
  });
  renderDocuments(snapshot, state, documentActions.selectDocument);
  renderReview(snapshot, state, {
    select: documentActions.selectReview,
    save: documentActions.saveCandidate,
    approve: documentActions.approveCandidate,
    remove: documentActions.deleteCandidate,
  });
  renderLibraries(snapshot, state.tree, state.treeError, {
    openProject: showProjectLibrary,
    openAction: openTreeAction,
    onRebuilt: (result) => {
      state.tree = result.tree;
      const receipt = result.materialization;
      byId("updated").textContent = [
        `分组索引${receipt.status === "REUSED" ? "无需更新" : "已重建"}`,
        `${receipt.documents} 文件`,
        `${receipt.chunks} 分块`,
      ].join(" · ");
      renderAll();
    },
    onError: (error) => {
      byId("updated").textContent = `分组索引失败：${error.message}`;
    },
  });
  renderPeerWorkspace(state);
  renderSessions(snapshot);
  byId("rail-candidate-count").textContent = snapshot
    ? String(snapshot.counts.candidate)
    : "–";
  const error = byId("snapshot-error");
  error.hidden = !state.error;
  error.textContent = state.error;
}
async function refresh() {
  if (state.loading) return;
  state.loading = true;
  state.error = "";
  byId("updated").textContent = state.snapshot ? "刷新中，保留当前数据" : "正在读取快照";
  renderAll();
  try {
    const [snapshotResult, treeResult, peerResult] = await Promise.allSettled([
      api.snapshot(), api.tree(), api.peers(),
    ]);
    if (treeResult.status === "fulfilled") {
      state.tree = treeResult.value.tree;
      state.treeError = "";
    } else {
      state.treeError = treeResult.reason.message;
    }
    if (peerResult.status === "fulfilled") {
      state.peer = peerResult.value;
      state.peerError = "";
    } else {
      state.peerError = peerResult.reason.message;
    }
    if (snapshotResult.status === "rejected") throw snapshotResult.reason;
    state.snapshot = snapshotResult.value;
    state.stale = false;
    const conflicts = await detectDraftConflicts(api, state);
    const refreshed = `已刷新 ${new Date().toLocaleTimeString(
      "zh-CN",
      { hour12: false },
    )}`;
    byId("updated").textContent = conflicts
      ? `${refreshed}，检测到外部更新，草稿未覆盖`
      : state.drafts.size ? `${refreshed}，已保留未保存草稿` : refreshed;
  } catch (error) {
    state.stale = Boolean(state.snapshot);
    state.error = state.stale
      ? `刷新失败：${error.message}；显示上次成功数据。`
      : `无法读取知识库：${error.message}`;
    byId("updated").textContent = state.stale ? "显示旧数据" : "读取失败";
  } finally { state.loading = false; renderAll(); }
}
function deleteDraft(item) {
  state.drafts.delete(item.path);
}
function bindControls() {
  document.addEventListener("click", (event) => {
    const control = event.target.closest("[data-route]");
    if (control) router.go(control.dataset.route);
  });
  byId("refresh").onclick = refresh;
  byId("open-global-library").onclick = showGlobalLibrary;
  byId("new-root-library").onclick = () => {
    if (state.tree) openTreeAction("create", null, state.tree);
  };
  byId("save-candidate").onclick = () => state.selected
    && documentActions.saveCandidate(
      state.selected.item,
      byId("content").value,
    );
  byId("approve-candidate").onclick = () => state.selected
    && documentActions.approveCandidate(state.selected.item);
  byId("delete-candidate").onclick = () => state.selected
    && documentActions.deleteCandidate(state.selected.item);
  byId("document-filter").oninput = (event) => {
    state.documentFilter = event.target.value;
    renderAll();
  };
  document.querySelectorAll("[data-document-state]").forEach((node) => {
    node.onclick = () => {
      state.documentState = node.dataset.documentState;
      document.querySelectorAll("[data-document-state]")
        .forEach((buttonNode) => {
          buttonNode.setAttribute(
            "aria-pressed",
            String(buttonNode === node),
          );
        });
      renderAll();
    };
  });
  byId("theme-toggle").onclick = () => {
    setTheme(NEXT_THEME[document.body.dataset.theme]);
  };
  byId("rail-toggle").onclick = () => {
    const shell = document.querySelector(".app-shell");
    shell.classList.toggle("rail-open");
    byId("rail-toggle").setAttribute(
      "aria-expanded",
      String(shell.classList.contains("rail-open")),
    );
  };
  byId("bottom-more").onclick = () => byId("rail-toggle").click();
  bindCommandPalette(router, documentActions.selectDocument);
}
state.intakeResults = restoreIntakeResults();
documentActions = createDocumentActions(api, state, {
  deleteDraft,
  refresh,
  render: renderAll,
});
setTheme(restoreTheme());
router = createRouter((route) => showRoute(route));
bindLibraryDialog();
bindTreeDialog((tree) => {
  state.tree = tree;
  state.treeError = "";
  byId("updated").textContent = `知识库树已更新至修订 ${tree.revision}`;
  renderAll();
});
bindPeerWorkspace(state, renderAll);
bindControls();
bindIntake(state, refresh);
refresh();
setInterval(refresh, 10_000);
