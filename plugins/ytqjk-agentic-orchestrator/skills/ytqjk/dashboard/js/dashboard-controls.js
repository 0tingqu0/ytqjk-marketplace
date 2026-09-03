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
const DARK_SCHEME_QUERY = "(prefers-color-scheme: dark)";
let themeMedia;
let themeMediaBound = false;

function normalizeTheme(theme) {
  return Object.hasOwn(THEME_LABELS, theme) ? theme : "system";
}

export function resolveTheme(theme, prefersDark = false) {
  const normalized = normalizeTheme(theme);
  if (normalized !== "system") return normalized;
  return prefersDark ? "dark" : "light";
}

function applyTheme(theme, persist) {
  const normalized = normalizeTheme(theme);
  themeMedia ||= window.matchMedia?.(DARK_SCHEME_QUERY);
  const resolved = resolveTheme(normalized, themeMedia?.matches);
  document.body.dataset.theme = normalized;
  document.body.dataset.resolvedTheme = resolved;
  document.documentElement.style.colorScheme = resolved;
  byId("theme-toggle")?.replaceChildren(
    document.createTextNode(`主题：${THEME_LABELS[normalized]}`),
  );
  byId("theme-toggle")?.setAttribute(
    "aria-label", `切换主题，当前为${THEME_LABELS[normalized]}主题`,
  );
  if (persist) saveTheme(normalized);
}

export function setTheme(theme) {
  applyTheme(theme, true);
}

function bindSystemTheme() {
  themeMedia ||= window.matchMedia?.(DARK_SCHEME_QUERY);
  if (!themeMedia || themeMediaBound) return;
  const sync = () => {
    if (document.body.dataset.theme === "system") {
      applyTheme("system", false);
    }
  };
  themeMedia.addEventListener?.("change", sync);
  if (!themeMedia.addEventListener) themeMedia.addListener?.(sync);
  themeMediaBound = true;
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
  bindSystemTheme();
  bindRail();
  bindCommandPalette(router, documentActions.selectDocument);
}
