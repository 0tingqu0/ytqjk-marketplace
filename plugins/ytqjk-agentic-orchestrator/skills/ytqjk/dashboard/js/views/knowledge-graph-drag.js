const DRAG_THRESHOLD = 3;
const MAX_STEP = 8;
const STOP_SPEED = 0.04;

function clamp(value, minimum, maximum) {
  return Math.max(minimum, Math.min(maximum, value));
}

function graphPoint(svg, event) {
  const box = svg.getAttribute("viewBox").split(/\s+/).map(Number);
  const bounds = svg.getBoundingClientRect();
  return {
    x: box[0] + ((event.clientX - bounds.left) / bounds.width) * box[2],
    y: box[1] + ((event.clientY - bounds.top) / bounds.height) * box[3],
  };
}

function visibleNodes(nodeElements) {
  return new Set([...nodeElements].filter(([, element]) => (
    !element.classList.contains("is-filtered-out")
  )).map(([nodeId]) => nodeId));
}

function graphNeighborhood(graph, rootId, visible, depth = 2) {
  const result = new Set([rootId]);
  let frontier = new Set([rootId]);
  for (let level = 0; level < depth; level += 1) {
    const next = new Set();
    graph.edges.forEach((edge) => {
      if (!visible.has(edge.source) || !visible.has(edge.target)) return;
      if (frontier.has(edge.source) && !result.has(edge.target)) {
        next.add(edge.target);
      }
      if (frontier.has(edge.target) && !result.has(edge.source)) {
        next.add(edge.source);
      }
    });
    next.forEach((nodeId) => result.add(nodeId));
    frontier = next;
  }
  return result;
}

function movePoint(point, dx, dy, movable, pinnedId) {
  if (!point || point.id === pinnedId || !movable.has(point.id)) return;
  point.vx += dx;
  point.vy += dy;
}

export function stepGraphPhysics(
  points, edges, movable, pinnedId = "", heat = 1,
) {
  const list = [...points.values()];
  edges.forEach((edge) => {
    const source = points.get(edge.source);
    const target = points.get(edge.target);
    if (!source || !target) return;
    const dx = target.x - source.x;
    const dy = target.y - source.y;
    const length = Math.max(1, Math.hypot(dx, dy));
    const pull = (length - edge.length) * 0.014 * heat;
    movePoint(source, (dx / length) * pull, (dy / length) * pull, movable, pinnedId);
    movePoint(target, -(dx / length) * pull, -(dy / length) * pull, movable, pinnedId);
  });
  for (let left = 0; left < list.length; left += 1) {
    for (let right = left + 1; right < list.length; right += 1) {
      const first = list[left];
      const second = list[right];
      const dx = second.x - first.x || 0.01;
      const dy = second.y - first.y;
      const distanceSquared = dx * dx + dy * dy;
      if (distanceSquared > 1764) continue;
      const distance = Math.max(1, Math.sqrt(distanceSquared));
      const push = (42 - distance) * 0.028 * heat;
      movePoint(first, -(dx / distance) * push, -(dy / distance) * push, movable, pinnedId);
      movePoint(second, (dx / distance) * push, (dy / distance) * push, movable, pinnedId);
    }
  }
  let maximumSpeed = 0;
  list.forEach((point) => {
    if (point.id === pinnedId || !movable.has(point.id)) {
      point.vx = 0;
      point.vy = 0;
      return;
    }
    point.vx *= 0.84;
    point.vy *= 0.84;
    point.x = clamp(point.x + clamp(point.vx, -MAX_STEP, MAX_STEP), 24, 876);
    point.y = clamp(point.y + clamp(point.vy, -MAX_STEP, MAX_STEP), 24, 546);
    maximumSpeed = Math.max(maximumSpeed, Math.hypot(point.vx, point.vy));
  });
  return maximumSpeed;
}

function renderPositions(nodeElements, edgeElements, labelElements, points) {
  nodeElements.forEach((element, nodeId) => {
    const point = points.get(nodeId);
    if (!point) return;
    element.setAttribute("transform", `translate(${point.x.toFixed(2)} ${point.y.toFixed(2)})`);
    element.dataset.x = point.x.toFixed(2);
    element.dataset.y = point.y.toFixed(2);
  });
  edgeElements.forEach((element, edgeId) => {
    const source = points.get(element.dataset.source);
    const target = points.get(element.dataset.target);
    if (!source || !target) return;
    element.querySelectorAll("line").forEach((line) => {
      line.setAttribute("x1", source.x.toFixed(2));
      line.setAttribute("y1", source.y.toFixed(2));
      line.setAttribute("x2", target.x.toFixed(2));
      line.setAttribute("y2", target.y.toFixed(2));
    });
    const label = labelElements.get(edgeId);
    label?.setAttribute("x", ((source.x + target.x) / 2).toFixed(2));
    label?.setAttribute("y", ((source.y + target.y) / 2 - 5).toFixed(2));
  });
}

function prefersReducedMotion() {
  return globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
}

export function bindGraphNodeDrag(svg, target, graph, layout) {
  const nodeElements = new Map([...svg.querySelectorAll("[data-node]")].map(
    (element) => [element.dataset.node, element],
  ));
  const edgeElements = new Map([...svg.querySelectorAll(".semantic-edge-link")].map(
    (element) => [element.dataset.edge, element],
  ));
  const labelElements = new Map([...svg.querySelectorAll(".semantic-edge-label")].map(
    (element) => [element.dataset.edge, element],
  ));
  const points = new Map(graph.nodes.map((node) => {
    const position = layout.positions.get(node.id);
    return [node.id, Object.assign(position, {
      id: node.id,
      vx: 0,
      vy: 0,
      cluster: layout.nodeClusters.get(node.id),
    })];
  }));
  let activeEdges = [];
  let drag = null;
  let frame = 0;
  let heat = 0;
  let movable = new Set();
  let suppressClickUntil = 0;

  function finishMotion() {
    target.classList.remove("is-graph-settling");
    heat = 0;
  }

  function animate() {
    frame = 0;
    const pinnedId = drag?.moved ? drag.nodeId : "";
    const speed = stepGraphPhysics(
      points, activeEdges, movable, pinnedId, heat,
    );
    renderPositions(nodeElements, edgeElements, labelElements, points);
    if (drag?.moved) {
      heat = Math.max(heat, 0.82);
    } else {
      heat *= 0.93;
    }
    if (drag?.moved || heat > 0.015 || speed > STOP_SPEED) {
      frame = requestAnimationFrame(animate);
    } else {
      finishMotion();
    }
  }

  function schedule() {
    if (!frame) frame = requestAnimationFrame(animate);
  }

  function prepare(nodeId) {
    const visible = visibleNodes(nodeElements);
    movable = graphNeighborhood(graph, nodeId, visible);
    activeEdges = graph.edges.filter((edge) => (
      visible.has(edge.source) && visible.has(edge.target)
    )).map((edge) => {
      const source = points.get(edge.source);
      const targetPoint = points.get(edge.target);
      return {
        source: edge.source,
        target: edge.target,
        length: Math.max(36, Math.hypot(
          targetPoint.x - source.x, targetPoint.y - source.y,
        )),
      };
    });
  }

  function pointerDown(event) {
    if (event.button !== 0) return;
    event.preventDefault();
    event.stopPropagation();
    const element = event.currentTarget;
    const point = points.get(element.dataset.node);
    if (!point) return;
    prepare(point.id);
    drag = {
      element,
      nodeId: point.id,
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      lastX: point.x,
      lastY: point.y,
      lastTime: event.timeStamp,
      velocityX: 0,
      velocityY: 0,
      moved: false,
    };
    element.focus({ preventScroll: true });
    element.setPointerCapture?.(event.pointerId);
    element.classList.add("is-dragging");
    target.classList.add("is-node-dragging");
  }

  function pointerMove(event) {
    if (!drag || drag.pointerId !== event.pointerId) return;
    const distance = Math.hypot(
      event.clientX - drag.startX, event.clientY - drag.startY,
    );
    if (!drag.moved && distance < DRAG_THRESHOLD) return;
    event.preventDefault();
    drag.moved = true;
    const next = graphPoint(svg, event);
    const point = points.get(drag.nodeId);
    const elapsed = Math.max(8, event.timeStamp - drag.lastTime);
    drag.velocityX = (next.x - drag.lastX) / elapsed;
    drag.velocityY = (next.y - drag.lastY) / elapsed;
    drag.lastX = next.x;
    drag.lastY = next.y;
    drag.lastTime = event.timeStamp;
    point.x = clamp(next.x, 24, layout.width - 24);
    point.y = clamp(next.y, 24, layout.height - 24);
    renderPositions(nodeElements, edgeElements, labelElements, points);
    if (!prefersReducedMotion()) {
      heat = 1;
      schedule();
    }
  }

  function pointerUp(event) {
    if (!drag || drag.pointerId !== event.pointerId) return;
    const released = drag;
    drag = null;
    released.element.classList.remove("is-dragging");
    target.classList.remove("is-node-dragging");
    if (released.element.hasPointerCapture?.(event.pointerId)) {
      released.element.releasePointerCapture(event.pointerId);
    }
    if (!released.moved) return;
    suppressClickUntil = Date.now() + 250;
    const point = points.get(released.nodeId);
    point.vx = clamp(released.velocityX * 7, -6, 6);
    point.vy = clamp(released.velocityY * 7, -6, 6);
    target.classList.add("is-graph-settling");
    heat = 1;
    if (prefersReducedMotion()) {
      for (let index = 0; index < 36; index += 1) {
        stepGraphPhysics(points, activeEdges, movable, "", heat);
        heat *= 0.9;
      }
      renderPositions(nodeElements, edgeElements, labelElements, points);
      finishMotion();
      return;
    }
    schedule();
  }

  function blockDraggedClick(event) {
    if (Date.now() >= suppressClickUntil) return;
    event.preventDefault();
    event.stopImmediatePropagation();
  }

  nodeElements.forEach((element) => {
    element.addEventListener("pointerdown", pointerDown);
    element.addEventListener("pointermove", pointerMove);
    element.addEventListener("pointerup", pointerUp);
    element.addEventListener("pointercancel", pointerUp);
    element.addEventListener("click", blockDraggedClick, true);
  });
  return {
    destroy() {
      cancelAnimationFrame(frame);
      target.classList.remove("is-node-dragging", "is-graph-settling");
      nodeElements.forEach((element) => {
        element.classList.remove("is-dragging");
        element.removeEventListener("pointerdown", pointerDown);
        element.removeEventListener("pointermove", pointerMove);
        element.removeEventListener("pointerup", pointerUp);
        element.removeEventListener("pointercancel", pointerUp);
        element.removeEventListener("click", blockDraggedClick, true);
      });
    },
  };
}
