export const byId = (id) => document.getElementById(id);

export function text(tag, value, className = "") {
  const node = document.createElement(tag);
  if (className) node.className = className;
  node.textContent = String(value ?? "");
  return node;
}

export function button(label, className = "") {
  const node = text("button", label, className);
  node.type = "button";
  return node;
}

export function icon(name, className = "") {
  const node = text("i", "", `ph ${name} ${className}`.trim());
  node.setAttribute("aria-hidden", "true");
  return node;
}

export function clear(node, children = []) {
  node.replaceChildren(...children);
}

export function formatBytes(value) {
  const units = ["B", "KiB", "MiB", "GiB"];
  let amount = Number(value) || 0;
  let index = 0;
  while (amount >= 1024 && index < units.length - 1) {
    amount /= 1024;
    index += 1;
  }
  const formatter = new Intl.NumberFormat(
    "zh-CN",
    { maximumFractionDigits: 1 },
  );
  return `${formatter.format(amount)} ${units[index]}`;
}

export function formatTime(value) {
  if (!value) return "未索引";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "未索引"
    : date.toLocaleString("zh-CN", { hour12: false });
}

export function stateLabel(item) {
  return item.state === "candidate" ? "CANDIDATE" : item.label;
}
