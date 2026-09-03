import assert from "node:assert/strict";
import test from "node:test";

import { bindGraphNodeDrag } from "../views/knowledge-graph-drag.js";

function fakeClassList() {
  const values = new Set();
  return {
    add(...names) { names.forEach((name) => values.add(name)); },
    contains(name) { return values.has(name); },
    remove(...names) { names.forEach((name) => values.delete(name)); },
    toggle(name, force) {
      if (force === undefined ? !values.has(name) : force) values.add(name);
      else values.delete(name);
    },
  };
}

function denseInteractionGraph() {
  const nodes = Array.from({ length: 66 }, (_, index) => ({
    id: `node:${index}`,
    type: index < 6 ? "document" : "entity",
  }));
  const edges = [
    ...Array.from({ length: 66 }, (_, index) => ({
      source: index,
      target: (index + 1) % 66,
    })),
    ...Array.from({ length: 66 }, (_, index) => ({
      source: index,
      target: (index + 7) % 66,
    })),
    ...Array.from({ length: 48 }, (_, index) => ({
      source: index,
      target: (index + 17) % 66,
    })),
  ].map((edge, index) => ({
    id: `edge:${index}`,
    source: `node:${edge.source}`,
    target: `node:${edge.target}`,
    type: "mentions",
  }));
  const positions = new Map(nodes.map((node, index) => [node.id, {
    x: 60 + (index % 11) * 70,
    y: 60 + Math.floor(index / 11) * 80,
  }]));
  return {
    graph: { nodes, edges },
    layout: {
      width: 900,
      height: 570,
      positions,
      nodeClusters: new Map(nodes.map((node) => [node.id, "all"])),
    },
  };
}

function interactionHarness(graph, layout) {
  const listeners = new Map();
  const nodeTransformWrites = new Map();
  let edgeCoordinateWrites = 0;
  const nodeElements = graph.nodes.map((node) => {
    const position = layout.positions.get(node.id);
    return {
      classList: fakeClassList(),
      dataset: {
        node: node.id,
        x: position.x.toFixed(2),
        y: position.y.toFixed(2),
      },
      focus() {},
      querySelector(selector) {
        if (selector !== ".graph-node-hit") return null;
        return { getAttribute() { return "22"; } };
      },
      setAttribute(name) {
        if (name !== "transform") return;
        nodeTransformWrites.set(
          node.id,
          (nodeTransformWrites.get(node.id) || 0) + 1,
        );
      },
    };
  });
  const edgeElements = graph.edges.map((edge) => {
    const lines = Array.from({ length: 2 }, () => ({
      setAttribute(name) {
        if (["x1", "y1", "x2", "y2"].includes(name)) {
          edgeCoordinateWrites += 1;
        }
      },
    }));
    return {
      dataset: {
        edge: edge.id,
        source: edge.source,
        target: edge.target,
      },
      querySelectorAll(selector) { return selector === "line" ? lines : []; },
    };
  });
  const svg = {
    addEventListener(type, listener) { listeners.set(type, listener); },
    cancelPointerCapture() {},
    createSVGPoint() {
      return {
        x: 0,
        y: 0,
        matrixTransform() { return { x: this.x, y: this.y }; },
      };
    },
    getScreenCTM() { return { inverse() { return {}; } }; },
    hasPointerCapture() { return true; },
    querySelectorAll(selector) {
      if (selector === "[data-node]") return nodeElements;
      if (selector === ".semantic-edge-link") return edgeElements;
      if (selector === ".semantic-edge-label") return [];
      if (selector === ".semantic-node:not(.is-filtered-out)") {
        return nodeElements;
      }
      return [];
    },
    releasePointerCapture() {},
    removeEventListener(type) { listeners.delete(type); },
    setPointerCapture() {},
  };
  return {
    edgeCoordinateWrites: () => edgeCoordinateWrites,
    listener: (type) => listeners.get(type),
    nodeElement: (index) => nodeElements[index],
    nodeTransformWrites,
    svg,
    target: { classList: fakeClassList() },
  };
}

function pointerEvent(overrides = {}) {
  return {
    button: 0,
    clientX: 60,
    clientY: 60,
    pointerId: 1,
    timeStamp: 0,
    preventDefault() {},
    stopImmediatePropagation() {},
    ...overrides,
  };
}

test("drag pointer bursts commit coordinates once with the latest position", () => {
  const previousAnimationFrame = globalThis.requestAnimationFrame;
  const previousCancelFrame = globalThis.cancelAnimationFrame;
  const previousMatchMedia = globalThis.matchMedia;
  const frames = new Map();
  let nextFrame = 1;
  globalThis.requestAnimationFrame = (callback) => {
    const identifier = nextFrame;
    nextFrame += 1;
    frames.set(identifier, callback);
    return identifier;
  };
  globalThis.cancelAnimationFrame = (identifier) => frames.delete(identifier);
  globalThis.matchMedia = () => ({ matches: false });

  const { graph, layout } = denseInteractionGraph();
  const harness = interactionHarness(graph, layout);
  const binding = bindGraphNodeDrag(
    harness.svg,
    harness.target,
    graph,
    layout,
  );
  const dragged = harness.nodeElement(0);
  let lastX = 0;
  let lastY = 0;

  try {
    harness.listener("pointerdown")(pointerEvent({ target: dragged }));
    for (let index = 0; index < 40; index += 1) {
      lastX = 120 + index;
      lastY = 90 + index / 2;
      harness.listener("pointermove")(pointerEvent({
        clientX: lastX,
        clientY: lastY,
        target: harness.svg,
        timeStamp: 16 + index,
      }));
    }

    assert.equal(frames.size, 1, "a pointer burst should queue one frame");
    assert.equal(
      harness.nodeTransformWrites.get("node:0") || 0,
      0,
      "coordinate rendering must wait for the queued frame",
    );
    assert.equal(
      harness.edgeCoordinateWrites(),
      0,
      "edge geometry must not be rewritten for every pointer event",
    );

    const [[identifier, renderFrame]] = frames;
    frames.delete(identifier);
    renderFrame(16.67);

    assert.equal(harness.nodeTransformWrites.get("node:0"), 1);
    assert.ok(
      harness.edgeCoordinateWrites() > 0,
      "incident edges must follow the dragged node",
    );
    assert.ok(
      harness.edgeCoordinateWrites() < graph.edges.length * 2 * 4,
      "one frame must update only the active edge subset",
    );
    assert.ok(
      harness.nodeTransformWrites.size < graph.nodes.length,
      "one frame must update only the movable node subset",
    );
    assert.equal(dragged.dataset.x, lastX.toFixed(2));
    assert.equal(dragged.dataset.y, lastY.toFixed(2));

    let idleFrames = 0;
    while (frames.size && idleFrames < 180) {
      const [[nextIdentifier, nextFrameCallback]] = frames;
      frames.delete(nextIdentifier);
      nextFrameCallback(16.67 + idleFrames * 16.67);
      idleFrames += 1;
    }
    assert.ok(
      idleFrames < 180,
      "a held but stationary node must stop requesting animation frames",
    );
    harness.listener("pointerup")(pointerEvent({ timeStamp: 200 }));
    assert.equal(frames.size, 1, "release should start a settling frame");
    harness.listener("pointerdown")(pointerEvent({
      clientX: 130,
      clientY: 60,
      pointerId: 2,
      target: harness.nodeElement(1),
      timeStamp: 220,
    }));
    assert.equal(frames.size, 0, "new input must interrupt settling");
    assert.equal(harness.target.classList.contains("is-graph-settling"), false);
    harness.listener("pointermove")(pointerEvent({
      clientX: 160, pointerId: 2, target: harness.svg, timeStamp: 240,
    }));
    assert.equal(frames.size, 1);
    harness.listener("pointercancel")(pointerEvent({
      pointerId: 2, target: harness.svg, timeStamp: 250, type: "pointercancel",
    }));
    assert.equal(frames.size, 0, "pointer cancellation must not start inertia");
    assert.equal(harness.target.classList.contains("is-node-dragging"), false);
  } finally {
    binding.destroy();
    if (previousAnimationFrame === undefined) {
      delete globalThis.requestAnimationFrame;
    } else globalThis.requestAnimationFrame = previousAnimationFrame;
    if (previousCancelFrame === undefined) {
      delete globalThis.cancelAnimationFrame;
    } else globalThis.cancelAnimationFrame = previousCancelFrame;
    if (previousMatchMedia === undefined) delete globalThis.matchMedia;
    else globalThis.matchMedia = previousMatchMedia;
  }
});

test("reduced motion keeps the dragged node at its direct drop point", () => {
  const previousAnimationFrame = globalThis.requestAnimationFrame;
  const previousCancelFrame = globalThis.cancelAnimationFrame;
  const previousMatchMedia = globalThis.matchMedia;
  const frames = [];
  globalThis.requestAnimationFrame = (callback) => {
    frames.push(callback);
    return frames.length;
  };
  globalThis.cancelAnimationFrame = () => {};
  globalThis.matchMedia = () => ({ matches: true });
  const { graph, layout } = denseInteractionGraph();
  const harness = interactionHarness(graph, layout);
  const binding = bindGraphNodeDrag(harness.svg, harness.target, graph, layout);
  const dragged = harness.nodeElement(0);

  try {
    harness.listener("pointerdown")(pointerEvent({ target: dragged }));
    harness.listener("pointermove")(pointerEvent({
      clientX: 180,
      clientY: 120,
      target: harness.svg,
      timeStamp: 16,
    }));
    frames.shift()(16.67);
    harness.listener("pointerup")(pointerEvent({
      clientX: 180,
      clientY: 120,
      target: harness.svg,
      timeStamp: 32,
    }));

    assert.equal(dragged.dataset.x, "180.00");
    assert.equal(dragged.dataset.y, "120.00");
    assert.equal(frames.length, 0);
  } finally {
    binding.destroy();
    if (previousAnimationFrame === undefined) {
      delete globalThis.requestAnimationFrame;
    } else globalThis.requestAnimationFrame = previousAnimationFrame;
    if (previousCancelFrame === undefined) {
      delete globalThis.cancelAnimationFrame;
    } else globalThis.cancelAnimationFrame = previousCancelFrame;
    if (previousMatchMedia === undefined) delete globalThis.matchMedia;
    else globalThis.matchMedia = previousMatchMedia;
  }
});
