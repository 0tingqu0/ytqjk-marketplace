const MIN_RADIUS = 72;
const MAX_RADIUS = 180;
const PADDING = 32;

function clusterLayout(id, label, members) {
  const xValues = members.map((member) => Number(member.dataset.x));
  const yValues = members.map((member) => Number(member.dataset.y));
  const x = (Math.min(...xValues) + Math.max(...xValues)) / 2;
  const y = (Math.min(...yValues) + Math.max(...yValues)) / 2;
  const radius = Math.min(MAX_RADIUS, Math.max(
    MIN_RADIUS,
    ...members.map((member) => Math.hypot(
      Number(member.dataset.x) - x,
      Number(member.dataset.y) - y,
    ) + PADDING),
  ));
  return { id, label, count: members.length, x, y, radius };
}

export function visibleGraphClusters(nodeElements, clusterLabels) {
  const grouped = new Map();
  [...nodeElements].forEach((element) => {
    const { cluster, x, y } = element.dataset;
    if (!cluster || element.classList.contains("is-filtered-out")) return;
    if (!Number.isFinite(Number(x)) || !Number.isFinite(Number(y))) return;
    if (!grouped.has(cluster)) grouped.set(cluster, []);
    grouped.get(cluster).push(element);
  });
  return [...grouped].filter(([, members]) => members.length >= 2).map(
    ([id, members]) => clusterLayout(
      id, clusterLabels.get(id) || id, members,
    ),
  );
}

function shortened(value, length = 22) {
  return value.length > length ? `${value.slice(0, length - 1)}…` : value;
}

export function syncGraphClusters(target) {
  if (!target?.querySelectorAll) return;
  const groups = new Map([...target.querySelectorAll(".semantic-cluster")].map(
    (group) => [group.dataset.cluster, group],
  ));
  const labels = new Map([...groups].map(([id, group]) => (
    [id, group.dataset.label || id]
  )));
  const layouts = new Map(visibleGraphClusters(
    target.querySelectorAll(".semantic-node"), labels,
  ).map((layout) => [layout.id, layout]));
  groups.forEach((group, id) => {
    const layout = layouts.get(id);
    group.classList.toggle("is-filtered-out", !layout);
    if (!layout) return;
    const halo = group.querySelector(".semantic-cluster-halo");
    const label = group.querySelector(".semantic-cluster-label");
    halo?.setAttribute("cx", layout.x.toFixed(2));
    halo?.setAttribute("cy", layout.y.toFixed(2));
    halo?.setAttribute("r", layout.radius.toFixed(2));
    label?.setAttribute("x", (layout.x - layout.radius + 14).toFixed(2));
    label?.setAttribute("y", (layout.y - layout.radius + 22).toFixed(2));
    if (label) label.textContent = `${shortened(layout.label)} · ${layout.count}`;
  });
}
