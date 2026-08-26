import { byId } from "./dom.js";

export function confirmAction(title, message, confirmLabel = "确认") {
  const dialog = byId("confirm-dialog");
  dialog.returnValue = "";
  dialog.onclick = (event) => {
    if (event.target === event.currentTarget) dialog.close("cancel");
  };
  byId("confirm-title").textContent = title;
  byId("confirm-message").textContent = message;
  byId("confirm-action").textContent = confirmLabel;
  return new Promise((resolve) => {
    const done = () => resolve(dialog.returnValue === "confirm");
    dialog.addEventListener("close", done, { once: true });
    dialog.showModal();
  });
}
