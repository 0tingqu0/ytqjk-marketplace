const WIDTH = 900;
const HEIGHT = 570;
const LAYOUT_CACHE = new WeakMap();

function hash(value) {
  let result = 2166136261;
  for (const character of value) {
    result ^= character.charCodeAt(0);
    result = Math.imul(result, 16777619);
  }
  return result >>> 0;
}

function pathCluster(node) {
  const segments = String(node.path || "").replaceAll("\\", "/").split("/")
    .filter(Boolean);
  const parent = segments.at(-2) || node.scope || "project";
  if (["approved", "verified"].includes(parent) && segments.length > 2) {
    return segments.at(-3);
  }
  return parent;
}

function clusterLabel(key) {
  return ({
    dashboard: "界面与服务",
    scripts: "脚本与工具",
    tests: "测试与质量",
    references: "规范与文档",
    ytqjk: "产品知识",
    other: "其他知识",
  })[key] || key;
}

function graphClusters(graph) {
  const nodes = new Map(graph.nodes.map((node) => [node.id, node]));
  const documents = graph.nodes.filter((node) => node.type === "document");
  const counts = new Map();
  documents.forEach((node) => {
    const key = pathCluster(node);
    counts.set(key, (counts.get(key) || 0) + 1);
  });
  const primary = new Set([...counts].sort((left, right) => (
    right[1] - left[1] || left[0].localeCompare(right[0])
  )).slice(0, 5).map(([key]) => key));
  const keys = new Map(documents.map((node) => {
    const key = pathCluster(node);
    return [node.id, primary.has(key) ? key : "other"];
  }));
  graph.nodes.filter((node) => node.type !== "document").forEach((node) => {
    const counts = new Map();
    graph.edges.forEach((edge) => {
      if (edge.source !== node.id && edge.target !== node.id) return;
      const neighbor = edge.source === node.id ? edge.target : edge.source;
      const neighborNode = nodes.get(neighbor) || {};
      const rawKey = keys.get(neighbor) || pathCluster(neighborNode);
      const key = primary.has(rawKey) ? rawKey : "other";
      if (key) counts.set(key, (counts.get(key) || 0) + 1);
    });
    const ranked = [...counts].sort((left, right) => (
      right[1] - left[1] || left[0].localeCompare(right[0])
    ));
    keys.set(node.id, ranked[0]?.[0] || "cross-document");
  });
  const unique = [...new Set(keys.values())].sort();
  const centers = new Map(unique.map((key, index) => {
    if (unique.length === 1) return [key, { x: WIDTH / 2, y: HEIGHT / 2 }];
    const angle = (index / unique.length) * Math.PI * 2 - Math.PI / 2;
    return [key, {
      x: WIDTH / 2 + Math.cos(angle) * 205,
      y: HEIGHT / 2 + Math.sin(angle) * 150,
    }];
  }));
  return { keys, centers };
}

function initialPoint(node, index, count, clusters) {
  const seed = hash(node.id);
  const angle = ((seed % 3600) / 3600) * Math.PI * 2;
  const center = clusters.centers.get(clusters.keys.get(node.id)) || {
    x: WIDTH / 2, y: HEIGHT / 2,
  };
  const band = node.type === "document" ? 1 : 0.68;
  const spread = 76 * band + (seed % 42);
  const jitter = ((index + 1) / Math.max(1, count)) * 10;
  return {
    x: center.x + Math.cos(angle) * (spread + jitter),
    y: center.y + Math.sin(angle) * (spread * 0.72 + jitter),
    vx: 0,
    vy: 0,
    cluster: clusters.keys.get(node.id),
  };
}

function repel(points) {
  for (let left = 0; left < points.length; left += 1) {
    for (let right = left + 1; right < points.length; right += 1) {
      const first = points[left];
      const second = points[right];
      let dx = second.x - first.x;
      let dy = second.y - first.y;
      if (!dx && !dy) dx = 0.01;
      const distance = Math.max(36, dx * dx + dy * dy);
      const force = 1450 / distance;
      const length = Math.sqrt(distance);
      dx /= length;
      dy /= length;
      first.vx -= dx * force;
      first.vy -= dy * force;
      second.vx += dx * force;
      second.vy += dy * force;
      if (length < 42) {
        const collision = (42 - length) * 0.055;
        first.vx -= dx * collision;
        first.vy -= dy * collision;
        second.vx += dx * collision;
        second.vy += dy * collision;
      }
    }
  }
}

function attract(points, indexById, edges) {
  edges.forEach((edge) => {
    const source = points[indexById.get(edge.source)];
    const target = points[indexById.get(edge.target)];
    if (!source || !target) return;
    const dx = target.x - source.x;
    const dy = target.y - source.y;
    const length = Math.max(1, Math.hypot(dx, dy));
    const preferred = edge.type === "mentions" ? 72
      : edge.type === "similar_to" ? 148 : 112;
    const force = (length - preferred) * 0.0028;
    source.vx += (dx / length) * force;
    source.vy += (dy / length) * force;
    target.vx -= (dx / length) * force;
    target.vy -= (dy / length) * force;
  });
}

function settle(points, centers) {
  points.forEach((point) => {
    const center = centers.get(point.cluster) || { x: WIDTH / 2, y: HEIGHT / 2 };
    point.vx += (center.x - point.x) * 0.0012;
    point.vy += (center.y - point.y) * 0.0012;
    point.vx *= 0.82;
    point.vy *= 0.82;
    point.x = Math.max(24, Math.min(WIDTH - 24, point.x + point.vx));
    point.y = Math.max(24, Math.min(HEIGHT - 24, point.y + point.vy));
  });
}

export function layoutKnowledgeGraph(graph) {
  const cached = LAYOUT_CACHE.get(graph);
  if (cached) return cached;
  const clusters = graphClusters(graph);
  const points = graph.nodes.map((node, index) => (
    initialPoint(node, index, graph.nodes.length, clusters)
  ));
  const indexById = new Map(graph.nodes.map((node, index) => [node.id, index]));
  for (let iteration = 0; iteration < 110; iteration += 1) {
    repel(points);
    attract(points, indexById, graph.edges);
    settle(points, clusters.centers);
  }
  const result = {
    width: WIDTH,
    height: HEIGHT,
    nodeClusters: clusters.keys,
    positions: new Map(graph.nodes.map((node, index) => [
      node.id,
      { x: points[index].x, y: points[index].y },
    ])),
    clusters: [...clusters.centers].map(([id, center]) => {
      const members = points.filter((point) => point.cluster === id);
      const radius = Math.min(180, Math.max(72, ...members.map((point) => (
        Math.hypot(point.x - center.x, point.y - center.y) + 32
      ))));
      return {
        id,
        label: clusterLabel(id),
        x: center.x,
        y: center.y,
        radius,
        count: members.length,
      };
    }),
  };
  LAYOUT_CACHE.set(graph, result);
  return result;
}
