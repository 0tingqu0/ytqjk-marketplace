import { api } from "./js/api.js";
import { restoreIntakeResults, restoreTheme } from "./js/store.js";
import { state } from "./js/store.js";
import { ROUTES, createRouter } from "./js/router.js";
import { byId } from "./js/ui/dom.js";
import { closeRail } from "./js/ui/rail.js";
import { bindLibraryDialog } from "./js/ui/library-dialog.js";
import { showProjectLibrary } from "./js/ui/library-dialog.js";
import { bindTreeDialog, openTreeAction } from "./js/ui/tree-dialog.js";
import { bindDashboardControls, setTheme } from "./js/dashboard-controls.js";
import { detectDraftConflicts } from "./js/draft-conflicts.js";
import { renderOverview } from "./js/views/overview.js";
import { createDocumentActions } from "./js/views/documents.js";
import { renderDocuments } from "./js/views/documents.js";
import { renderReview } from "./js/views/review.js";
import { renderLibraries } from "./js/views/libraries.js";
import { renderSessions } from "./js/views/sessions.js";
import { bindIntake } from "./js/views/intake.js";
import { bindKnowledgeGraphWorkbench } from "./js/views/knowledge-graph-workbench.js";
import { bindPeerWorkspace, renderPeerWorkspace } from "./js/peers/control.js";
import {
  loadKnowledgeGraph,
  sameData,
  shouldAutoRefresh,
} from "./js/refresh-policy.js";
let router, documentActions;
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
  closeRail();
  if (focus) byId("view-root").focus();
}
function renderAll() {
  const snapshot = state.snapshot;
  renderOverview(snapshot, state.stale, (item) => {
    documentActions.selectReview(item);
    router.go("review");
  }, state.knowledgeGraph, state.knowledgeGraphError);
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

function updateState(key, value) {
  if (sameData(state[key], value)) return false;
  state[key] = value;
  return true;
}

function updateError(key, value) {
  if (state[key] === value) return false;
  state[key] = value;
  return true;
}

async function refresh(silent = false) {
  if (state.loading) return;
  state.loading = true;
  const clearedError = updateError("error", "");
  let needsRender = !state.snapshot || clearedError;
  if (!silent) {
    byId("updated").textContent = state.snapshot
      ? "刷新中，保留当前数据"
      : "正在读取快照";
  }
  if (!state.snapshot) renderAll();
  try {
    const [snapshotResult, treeResult, peerResult, graphResult] = await Promise.allSettled([
      api.snapshot(), api.tree(), api.peers(), loadKnowledgeGraph(
        api,
        state.knowledgeGraph ? state.knowledgeGraphRevision : "",
      ),
    ]);
    if (treeResult.status === "fulfilled") {
      needsRender = updateState("tree", treeResult.value.tree) || needsRender;
      needsRender = updateError("treeError", "") || needsRender;
    } else {
      needsRender = updateError(
        "treeError", treeResult.reason.message,
      ) || needsRender;
    }
    if (peerResult.status === "fulfilled") {
      needsRender = updateState("peer", peerResult.value) || needsRender;
      needsRender = updateError("peerError", "") || needsRender;
    } else {
      needsRender = updateError(
        "peerError", peerResult.reason.message,
      ) || needsRender;
    }
    if (graphResult.status === "fulfilled") {
      if (graphResult.value.changed) {
        state.knowledgeGraph = graphResult.value.graph;
        needsRender = true;
      }
      state.knowledgeGraphRevision = graphResult.value.revision;
      needsRender = updateError("knowledgeGraphError", "") || needsRender;
    } else {
      needsRender = updateError(
        "knowledgeGraphError", graphResult.reason.message,
      ) || needsRender;
    }
    if (snapshotResult.status === "rejected") throw snapshotResult.reason;
    needsRender = updateState(
      "snapshot", snapshotResult.value,
    ) || needsRender;
    needsRender = state.stale || needsRender;
    state.stale = false;
    const conflicts = await detectDraftConflicts(api, state);
    needsRender = Boolean(conflicts) || needsRender;
    const refreshed = `已刷新 ${new Date().toLocaleTimeString(
      "zh-CN",
      { hour12: false },
    )}`;
    byId("updated").textContent = conflicts
      ? `${refreshed}，检测到外部更新，草稿未覆盖`
      : state.drafts.size ? `${refreshed}，已保留未保存草稿` : refreshed;
  } catch (error) {
    const stale = Boolean(state.snapshot);
    needsRender = state.stale !== stale || needsRender;
    state.stale = stale;
    const message = state.stale
      ? `刷新失败：${error.message}；显示上次成功数据。`
      : `无法读取知识库：${error.message}`;
    needsRender = updateError("error", message) || needsRender;
    byId("updated").textContent = state.stale ? "显示旧数据" : "读取失败";
  } finally {
    state.loading = false;
    if (needsRender) renderAll();
  }
}
function deleteDraft(item) {
  state.drafts.delete(item.path);
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
bindKnowledgeGraphWorkbench(api);
bindDashboardControls({ documentActions, renderAll, router });
byId("refresh").onclick = () => refresh();
bindIntake(state, refresh);
refresh();
document.addEventListener("visibilitychange", () => {
  if (shouldAutoRefresh(document.hidden)) refresh(true);
});
