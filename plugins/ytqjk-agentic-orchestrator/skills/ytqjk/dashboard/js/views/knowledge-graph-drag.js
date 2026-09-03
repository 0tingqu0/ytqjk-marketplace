import {
  graphPointFromEvent,
  nearestGraphElement,
} from "./knowledge-graph-hit-targets.js";
import { createGraphDragRenderer } from "./knowledge-graph-drag-render.js";
import { syncGraphClusters } from "./knowledge-graph-clusters.js";

const DRAG_THRESHOLD = 3;
const FLING_IDLE_THRESHOLD = 96;
const MAX_STEP = 8;
const STOP_SPEED = 0.04;

function clamp(value, minimum, maximum) {
  return Math.max(minimum, Math.min(maximum, value));
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
  points, edges, movable, pinnedId = "", heat = 1, participantIds = null,
) {
  const list = participantIds
    ? [...participantIds].map((nodeId) => points.get(nodeId)).filter(Boolean)
    : [...points.values()];
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
  points.forEach((point) => {
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

function prefersReducedMotion() {
  return globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
}

export function bindGraphNodeDrag(svg, target, graph, layout) {
  const renderer = createGraphDragRenderer(svg);
  const { nodeElements } = renderer;
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
  let participants = new Set();
  let suppressClickUntil = 0;

  function finishMotion() {
    target.classList.remove("is-graph-settling");
    heat = 0;
    syncGraphClusters(target);
  }

  function interruptMotion() {
    cancelAnimationFrame(frame);
    frame = 0;
    heat = 0;
    points.forEach((point) => {
      point.vx = 0;
      point.vy = 0;
    });
    target.classList.remove("is-graph-settling");
    syncGraphClusters(target);
  }

  function animate() {
    frame = 0;
    const pinnedId = drag?.moved ? drag.nodeId : "";
    const reducedDuringDrag = Boolean(drag?.moved && prefersReducedMotion());
    const speed = reducedDuringDrag ? 0 : stepGraphPhysics(
      points, activeEdges, movable, pinnedId, heat, participants,
    );
    renderer.render(points);
    if (drag?.moved) {
      if (reducedDuringDrag) {
        heat = 0;
        return;
      }
      heat *= 0.84;
    } else {
      heat *= 0.93;
    }
    if (heat > 0.015 || speed > STOP_SPEED) {
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
    participants = visible;
    movable = graphNeighborhood(graph, nodeId, visible);
    activeEdges = graph.edges.filter((edge) => (
      visible.has(edge.source) && visible.has(edge.target)
      && (movable.has(edge.source) || movable.has(edge.target))
    )).map((edge) => {
      const source = points.get(edge.source);
      const targetPoint = points.get(edge.target);
      return {
        id: edge.id,
        source: edge.source,
        target: edge.target,
        length: Math.max(36, Math.hypot(
          targetPoint.x - source.x, targetPoint.y - source.y,
        )),
      };
    });
    renderer.prepare(movable, activeEdges);
  }

  function pointerDown(event) {
    if (event.button !== 0 || drag) return;
    interruptMotion();
    const element = nearestGraphElement(
      svg, event, ".semantic-node:not(.is-filtered-out)",
    );
    if (!element) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    const point = points.get(element.dataset.node);
    if (!point) return;
    const pointer = graphPointFromEvent(svg, event);
    if (!pointer) return;
    prepare(point.id);
    drag = {
      element,
      nodeId: point.id,
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      offsetX: point.x - pointer.x,
      offsetY: point.y - pointer.y,
      lastX: point.x,
      lastY: point.y,
      lastTime: event.timeStamp,
      velocityX: 0,
      velocityY: 0,
      moved: false,
    };
    element.focus({ preventScroll: true });
    svg.setPointerCapture?.(event.pointerId);
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
    const pointer = graphPointFromEvent(svg, event);
    if (!pointer) return;
    const next = {
      x: pointer.x + drag.offsetX,
      y: pointer.y + drag.offsetY,
    };
    const point = points.get(drag.nodeId);
    const elapsed = Math.max(8, event.timeStamp - drag.lastTime);
    drag.velocityX = (next.x - drag.lastX) / elapsed;
    drag.velocityY = (next.y - drag.lastY) / elapsed;
    drag.lastX = next.x;
    drag.lastY = next.y;
    drag.lastTime = event.timeStamp;
    point.x = clamp(next.x, 24, layout.width - 24);
    point.y = clamp(next.y, 24, layout.height - 24);
    heat = 1;
    schedule();
  }

  function pointerUp(event) {
    if (!drag || drag.pointerId !== event.pointerId) return;
    const released = drag;
    drag = null;
    released.element.classList.remove("is-dragging");
    target.classList.remove("is-node-dragging");
    if (svg.hasPointerCapture?.(event.pointerId)) {
      svg.releasePointerCapture(event.pointerId);
    }
    if (event.type === "pointercancel") {
      renderer.render(points);
      interruptMotion();
      return;
    }
    if (!released.moved) return;
    suppressClickUntil = Date.now() + 250;
    const point = points.get(released.nodeId);
    target.classList.add("is-graph-settling");
    heat = 1;
    if (prefersReducedMotion()) {
      cancelAnimationFrame(frame);
      frame = 0;
      point.vx = 0;
      point.vy = 0;
      for (let index = 0; index < 36; index += 1) {
        stepGraphPhysics(
          points, activeEdges, movable, released.nodeId, heat, participants,
        );
        heat *= 0.9;
      }
      renderer.render(points);
      finishMotion();
      return;
    }
    const recentlyMoved = event.timeStamp - released.lastTime
      <= FLING_IDLE_THRESHOLD;
    point.vx = recentlyMoved ? clamp(released.velocityX * 7, -6, 6) : 0;
    point.vy = recentlyMoved ? clamp(released.velocityY * 7, -6, 6) : 0;
    schedule();
  }

  function blockDraggedClick(event) {
    if (Date.now() >= suppressClickUntil) return;
    event.preventDefault();
    event.stopImmediatePropagation();
  }

  svg.addEventListener("pointerdown", pointerDown);
  svg.addEventListener("pointermove", pointerMove);
  svg.addEventListener("pointerup", pointerUp);
  svg.addEventListener("pointercancel", pointerUp);
  svg.addEventListener("click", blockDraggedClick, true);
  return {
    destroy() {
      cancelAnimationFrame(frame);
      if (drag && svg.hasPointerCapture?.(drag.pointerId)) {
        svg.releasePointerCapture(drag.pointerId);
      }
      drag = null;
      target.classList.remove("is-node-dragging", "is-graph-settling");
      nodeElements.forEach((element) => element.classList.remove("is-dragging"));
      svg.removeEventListener("pointerdown", pointerDown);
      svg.removeEventListener("pointermove", pointerMove);
      svg.removeEventListener("pointerup", pointerUp);
      svg.removeEventListener("pointercancel", pointerUp);
      svg.removeEventListener("click", blockDraggedClick, true);
    },
  };
}
