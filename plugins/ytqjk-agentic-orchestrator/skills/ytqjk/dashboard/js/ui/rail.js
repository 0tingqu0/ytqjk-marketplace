import { byId } from "./dom.js";

const MOBILE_QUERY = "(max-width: 900px)";
const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  "[tabindex]:not([tabindex='-1'])",
].join(",");

let mobileMedia;
let railReturnFocus;

function shell() {
  return document.querySelector(".app-shell");
}

function isMobile() {
  mobileMedia ||= window.matchMedia?.(MOBILE_QUERY);
  return Boolean(mobileMedia?.matches);
}

function setRegionInert(node, inert, hideFromTree = false) {
  if (!node) return;
  node.inert = inert;
  if (inert && hideFromTree) node.setAttribute("aria-hidden", "true");
  else node.removeAttribute("aria-hidden");
}

function syncAccessibility(open) {
  const mobile = isMobile();
  const rail = byId("app-rail");
  const scrim = byId("rail-scrim");
  setRegionInert(rail, mobile && !open, true);
  setRegionInert(document.querySelector(".workspace"), mobile && open);
  setRegionInert(document.querySelector(".bottom-nav"), mobile && open);
  if (scrim) {
    scrim.hidden = !(mobile && open);
    scrim.setAttribute("aria-hidden", String(!(mobile && open)));
    scrim.tabIndex = -1;
  }
}

function focusCurrentRoute() {
  const rail = byId("app-rail");
  const current = rail.querySelector('[data-route][aria-current="page"]');
  (current || rail.querySelector(FOCUSABLE_SELECTOR))?.focus();
}

function setRailOpen(open, options = {}) {
  const appShell = shell();
  if (!appShell) return;
  const wasOpen = appShell.classList.contains("rail-open");
  if (open && !wasOpen) {
    railReturnFocus = options.returnFocus || document.activeElement;
  }
  appShell.classList.toggle("rail-open", open);
  document.body.classList.toggle("rail-navigation-open", open && isMobile());
  byId("rail-toggle")?.setAttribute("aria-expanded", String(open));
  byId("bottom-more")?.setAttribute("aria-expanded", String(open));
  syncAccessibility(open);
  if (open && isMobile() && options.focusRail !== false) {
    focusCurrentRoute();
  } else if (!open && wasOpen && options.restoreFocus !== false) {
    const fallback = byId("rail-toggle");
    const returnTarget = railReturnFocus?.isConnected ? railReturnFocus : fallback;
    returnTarget?.focus({ preventScroll: true });
  }
  if (!open) railReturnFocus = null;
}

function trapRailFocus(event) {
  if (event.key === "Escape") {
    event.preventDefault();
    event.stopPropagation();
    closeRail();
    return;
  }
  if (event.key !== "Tab" || !isMobile()) return;
  const rail = byId("app-rail");
  const focusable = [...rail.querySelectorAll(FOCUSABLE_SELECTOR)]
    .filter((node) => !node.hidden && node.getClientRects().length > 0);
  if (!focusable.length) return;
  const first = focusable[0];
  const last = focusable.at(-1);
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}

export function closeRail(options = {}) {
  setRailOpen(false, options);
}

export function bindRail() {
  const toggle = byId("rail-toggle");
  toggle.onclick = () => {
    setRailOpen(!shell().classList.contains("rail-open"));
  };
  byId("bottom-more").onclick = (event) => {
    setRailOpen(!shell().classList.contains("rail-open"), {
      returnFocus: event.currentTarget,
    });
  };
  byId("app-rail").addEventListener("keydown", trapRailFocus);
  byId("rail-scrim")?.addEventListener("click", () => closeRail());
  document.addEventListener("click", (event) => {
    if (!shell().classList.contains("rail-open")) return;
    const allowed = "#app-rail, #rail-toggle, #bottom-more";
    if (event.target.closest?.(allowed)) return;
    closeRail();
  });
  mobileMedia ||= window.matchMedia?.(MOBILE_QUERY);
  const syncViewport = () => {
    if (!mobileMedia?.matches) {
      setRailOpen(false, { restoreFocus: false });
      return;
    }
    const open = shell().classList.contains("rail-open");
    syncAccessibility(open);
    if (!open && byId("app-rail").contains(document.activeElement)) {
      byId("rail-toggle")?.focus({ preventScroll: true });
    }
  };
  mobileMedia?.addEventListener?.("change", syncViewport);
  if (!mobileMedia?.addEventListener) mobileMedia?.addListener?.(syncViewport);
  syncViewport();
}
