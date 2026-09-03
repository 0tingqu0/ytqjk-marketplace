import { ROUTES } from "./router.js";
import { state } from "./store.js";
import { byId, button, clear, text } from "./ui/dom.js";
import { closeRail } from "./ui/rail.js";

function entries(router, openDocument) {
  const commands = Object.entries(ROUTES).map(([route, [, label]]) => ({
    label,
    detail: "视图",
    run: () => router.go(route),
  }));
  (state.snapshot?.documents || []).forEach((item) => {
    commands.push({
      label: item.path,
      detail: item.label,
      run: () => {
        router.go("documents");
        openDocument(item);
      },
    });
  });
  return commands;
}

function findCommands(router, openDocument, rawQuery) {
  const query = rawQuery.trim().toLowerCase();
  return entries(router, openDocument).filter((item) => (
    `${item.label} ${item.detail}`.toLowerCase().includes(query)
  )).slice(0, 20);
}

function render(router, openDocument) {
  const matches = findCommands(
    router,
    openDocument,
    byId("command-filter").value,
  );
  const rows = matches.map((item) => {
    const row = button("", "command-row");
    row.append(text("span", item.label), text("small", item.detail));
    row.onclick = () => {
      byId("command-dialog").close();
      item.run();
    };
    return row;
  });
  clear(
    byId("command-results"),
    rows.length ? rows : [text("p", "没有匹配项。", "muted")],
  );
}

export function bindCommandPalette(router, openDocument) {
  let routePrefix = false;
  const dialog = byId("command-dialog");
  const blockedByModal = () => {
    const active = document.querySelector(
      'dialog[open], [aria-modal="true"]:not([hidden])',
    );
    return Boolean(active && active !== dialog);
  };
  const open = () => {
    if (blockedByModal()) return;
    render(router, openDocument);
    if (!dialog.open) dialog.showModal();
    byId("command-filter").focus();
  };
  byId("command-trigger").onclick = open;
  byId("shortcut-help").onclick = open;
  byId("close-command").onclick = () => dialog.close();
  dialog.onclick = (event) => {
    if (event.target === event.currentTarget) dialog.close();
  };
  byId("command-filter").oninput = () => render(router, openDocument);
  document.addEventListener("keydown", (event) => {
    const commandShortcut = (event.ctrlKey || event.metaKey)
      && event.key.toLowerCase() === "k";
    if (blockedByModal() && event.key !== "Escape") {
      if (commandShortcut) event.preventDefault();
      return;
    }
    const editing = /INPUT|TEXTAREA/.test(document.activeElement.tagName);
    const route = {
      o: "overview",
      i: "intake",
      d: "documents",
      r: "review",
      p: "libraries",
      s: "sessions",
    }[event.key.toLowerCase()];
    if (commandShortcut) {
      event.preventDefault();
      open();
    } else if (routePrefix && route && !editing) {
      event.preventDefault();
      routePrefix = false;
      router.go(route);
    } else if (event.key.toLowerCase() === "g" && !editing) {
      routePrefix = true;
      setTimeout(() => { routePrefix = false; }, 900);
    } else if (event.key === "/" && !editing) {
      event.preventDefault();
      const search = document.querySelector(
        `[data-view="${state.route}"] [data-view-search]`,
      );
      if (search) search.focus();
      else open();
    } else if (event.key === "Escape") {
      document.querySelectorAll("dialog[open]").forEach((dialog) => {
        dialog.close();
      });
      closeRail();
    }
  });
}
