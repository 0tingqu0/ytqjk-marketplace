import { bindCommandPalette } from "./command-palette.js";
import { saveTheme, state } from "./store.js";
import { showGlobalLibrary } from "./ui/library-dialog.js";
import { byId } from "./ui/dom.js";
import { bindRail } from "./ui/rail.js";
import { openTreeAction } from "./ui/tree-dialog.js";

const THEME_LABELS = {
  system: "系统", light: "浅色", dark: "暗色",
};
const NEXT_THEME = {
  system: "light", light: "dark", dark: "system",
};

export function setTheme(theme) {
  document.body.dataset.theme = theme;
  byId("theme-toggle").textContent = `主题：${THEME_LABELS[theme]}`;
  saveTheme(theme);
}

export function bindDashboardControls(context) {
  const { documentActions, renderAll, router } = context;
  document.addEventListener("click", (event) => {
    const control = event.target.closest("[data-route]");
    if (control) router.go(control.dataset.route);
  });
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
  bindRail();
  bindCommandPalette(router, documentActions.selectDocument);
}
