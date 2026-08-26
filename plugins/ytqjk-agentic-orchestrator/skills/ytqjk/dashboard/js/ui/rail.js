import { byId } from "./dom.js";


function shell() {
  return document.querySelector(".app-shell");
}

function setRailOpen(open) {
  shell().classList.toggle("rail-open", open);
  byId("rail-toggle").setAttribute("aria-expanded", String(open));
}

export function closeRail() {
  setRailOpen(false);
}

export function bindRail() {
  const toggle = byId("rail-toggle");
  toggle.onclick = () => {
    setRailOpen(!shell().classList.contains("rail-open"));
  };
  byId("bottom-more").onclick = () => toggle.click();
  document.addEventListener("click", (event) => {
    if (!shell().classList.contains("rail-open")) return;
    const allowed = "#app-rail, #rail-toggle, #bottom-more";
    if (event.target.closest?.(allowed)) return;
    closeRail();
  });
}
