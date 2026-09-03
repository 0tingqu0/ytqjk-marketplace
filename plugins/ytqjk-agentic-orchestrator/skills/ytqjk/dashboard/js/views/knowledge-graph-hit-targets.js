const TARGET_RADIUS_PX = 22;

export function nearestGraphNode(points, x, y) {
  return points.reduce((nearest, point) => {
    const distanceSquared = (point.x - x) ** 2 + (point.y - y) ** 2;
    if (nearest && nearest.distanceSquared < distanceSquared) return nearest;
    return { ...point, distanceSquared };
  }, null);
}

export function graphPointFromEvent(svg, event) {
  const matrix = svg.getScreenCTM();
  if (!matrix) return null;
  const screenPoint = svg.createSVGPoint();
  screenPoint.x = event.clientX;
  screenPoint.y = event.clientY;
  return screenPoint.matrixTransform(matrix.inverse());
}

export function nearestGraphElement(svg, event, selector) {
  const point = graphPointFromEvent(svg, event);
  if (!point) return null;
  const points = [...svg.querySelectorAll(selector)].map((element) => ({
    element,
    x: Number(element.dataset.graphX ?? element.dataset.x),
    y: Number(element.dataset.graphY ?? element.dataset.y),
  }));
  const nearest = nearestGraphNode(points, point.x, point.y);
  const radius = Number(
    nearest?.element.querySelector(".graph-node-hit")?.getAttribute("r"),
  );
  return nearest && nearest.distanceSquared <= radius ** 2
    ? nearest.element
    : null;
}

export function syncGraphNodeHitTargets(svg, selector = ".graph-node-hit") {
  const bounds = svg.getBoundingClientRect();
  const viewBox = svg.getAttribute("viewBox").split(/\s+/).map(Number);
  const scale = Math.min(
    bounds.width / viewBox[2], bounds.height / viewBox[3],
  );
  const radius = Number.isFinite(scale) && scale > 0
    ? TARGET_RADIUS_PX / scale
    : TARGET_RADIUS_PX;
  svg.querySelectorAll(selector).forEach((hit) => {
    hit.setAttribute("r", radius.toFixed(2));
  });
}

export function watchGraphNodeHitTargets(svg, selector = ".graph-node-hit") {
  const sync = () => syncGraphNodeHitTargets(svg, selector);
  sync();
  if (typeof ResizeObserver === "undefined") return () => {};
  const observer = new ResizeObserver(sync);
  observer.observe(svg);
  return () => observer.disconnect();
}
