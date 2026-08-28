const WIDTH = 900;
const HEIGHT = 570;

function hash(value) {
  let result = 2166136261;
  for (const character of value) {
    result ^= character.charCodeAt(0);
    result = Math.imul(result, 16777619);
  }
  return result >>> 0;
}

function initialPoint(node, index, count) {
  const seed = hash(node.id);
  const angle = ((seed % 3600) / 3600) * Math.PI * 2;
  const band = node.type === "document" ? 0.78 : 0.42 + (seed % 29) / 100;
  const spread = Math.min(WIDTH, HEIGHT) * band * 0.48;
  const jitter = ((index + 1) / Math.max(1, count)) * 22;
  return {
    x: WIDTH / 2 + Math.cos(angle) * (spread + jitter),
    y: HEIGHT / 2 + Math.sin(angle) * (spread * 0.68 + jitter),
    vx: 0,
    vy: 0,
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

function settle(points) {
  points.forEach((point) => {
    point.vx += (WIDTH / 2 - point.x) * 0.00045;
    point.vy += (HEIGHT / 2 - point.y) * 0.00045;
    point.vx *= 0.82;
    point.vy *= 0.82;
    point.x = Math.max(24, Math.min(WIDTH - 24, point.x + point.vx));
    point.y = Math.max(24, Math.min(HEIGHT - 24, point.y + point.vy));
  });
}

export function layoutKnowledgeGraph(graph) {
  const points = graph.nodes.map((node, index) => (
    initialPoint(node, index, graph.nodes.length)
  ));
  const indexById = new Map(graph.nodes.map((node, index) => [node.id, index]));
  for (let iteration = 0; iteration < 110; iteration += 1) {
    repel(points);
    attract(points, indexById, graph.edges);
    settle(points);
  }
  return {
    width: WIDTH,
    height: HEIGHT,
    positions: new Map(graph.nodes.map((node, index) => [
      node.id,
      { x: points[index].x, y: points[index].y },
    ])),
  };
}
