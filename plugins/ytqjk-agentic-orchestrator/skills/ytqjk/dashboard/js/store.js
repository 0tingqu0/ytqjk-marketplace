const THEME_KEY = "ytqjk-dashboard-theme";
const INTAKE_KEY = "ytqjk-last-intake-progress";

export const state = {
  snapshot: null,
  knowledgeGraph: null,
  knowledgeGraphRevision: "",
  knowledgeGraphError: "",
  tree: null,
  treeError: "",
  peer: null,
  peerError: "",
  peerStatus: "",
  peerDispatch: null,
  peerHealth: new Map(),
  peerRemoteLibraries: [],
  selected: null,
  reviewSelected: null,
  route: "overview",
  documentFilter: "",
  documentState: "all",
  loading: false,
  stale: false,
  error: "",
  intakeResults: [],
  drafts: new Map(),
};

export function restoreTheme() {
  try {
    return localStorage.getItem(THEME_KEY) || "system";
  } catch {
    return "system";
  }
}

export function saveTheme(theme) {
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch {
    /* Storage is optional. */
  }
}

export function restoreIntakeResults() {
  try {
    const saved = JSON.parse(localStorage.getItem(INTAKE_KEY) || "[]");
    return Array.isArray(saved) ? saved.slice(0, 12) : [];
  } catch { return []; }
}

export function saveIntakeResults(rows) {
  try {
    localStorage.setItem(
      INTAKE_KEY,
      JSON.stringify(rows.slice(0, 12)),
    );
  } catch {
    /* Storage is optional. */
  }
}
