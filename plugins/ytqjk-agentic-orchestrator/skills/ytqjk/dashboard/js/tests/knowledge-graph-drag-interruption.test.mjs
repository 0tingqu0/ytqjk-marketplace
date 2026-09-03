import assert from "node:assert/strict";
import test from "node:test";

import { bindGraphNodeDrag } from "../views/knowledge-graph-drag.js";

function classList() {
  const values = new Set();
  return {
    add(...names) { names.forEach((name) => values.add(name)); },
    contains(name) { return values.has(name); },
    remove(...names) { names.forEach((name) => values.delete(name)); },
    toggle(name, force) {
      if (force) values.add(name);
      else values.delete(name);
    },
  };
}

function node(id, x) {
  return {
    classList: classList(),
    dataset: { node: id, cluster: "group", x: String(x), y: "100" },
    focus() {},
    querySelector(selector) {
      return selector === ".graph-node-hit"
        ? { getAttribute() { return "22"; } }
        : null;
    },
    setAttribute() {},
  };
}

function pointerEvent(x, type = "pointermove") {
  return {
    button: 0,
    clientX: x,
    clientY: 100,
    pointerId: 1,
    preventDefault() {},
    stopImmediatePropagation() {},
    timeStamp: x,
    type,
  };
}

test("interrupting graph settling synchronizes the cluster halo", () => {
  const previousAnimationFrame = globalThis.requestAnimationFrame;
  const previousCancelFrame = globalThis.cancelAnimationFrame;
  const previousMatchMedia = globalThis.matchMedia;
  const frames = new Map();
  let nextFrame = 1;
  globalThis.requestAnimationFrame = (callback) => {
    frames.set(nextFrame, callback);
    nextFrame += 1;
    return nextFrame - 1;
  };
  globalThis.cancelAnimationFrame = (identifier) => frames.delete(identifier);
  globalThis.matchMedia = () => ({ matches: false });
  const nodes = [node("a", 100), node("b", 260)];
  const haloAttributes = new Map([["cx", "180.00"]]);
  const halo = {
    setAttribute(name, value) { haloAttributes.set(name, value); },
  };
  const cluster = {
    classList: classList(),
    dataset: { cluster: "group", label: "Group" },
    querySelector(selector) {
      if (selector === ".semantic-cluster-halo") return halo;
      return null;
    },
  };
  const edge = {
    dataset: { edge: "a:b", source: "a", target: "b" },
    querySelectorAll() { return []; },
  };
  const listeners = new Map();
  const svg = {
    addEventListener(type, listener) { listeners.set(type, listener); },
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
      if (selector === "[data-node]") return nodes;
      if (selector === ".semantic-node:not(.is-filtered-out)") return nodes;
      if (selector === ".semantic-edge-link") return [edge];
      return [];
    },
    releasePointerCapture() {},
    removeEventListener(type) { listeners.delete(type); },
    setPointerCapture() {},
  };
  const target = {
    classList: classList(),
    querySelectorAll(selector) {
      if (selector === ".semantic-cluster") return [cluster];
      if (selector === ".semantic-node") return nodes;
      return [];
    },
  };
  const graph = {
    nodes: [{ id: "a" }, { id: "b" }],
    edges: [{ id: "a:b", source: "a", target: "b" }],
  };
  const layout = {
    width: 900,
    height: 570,
    positions: new Map([["a", { x: 100, y: 100 }], ["b", { x: 260, y: 100 }]]),
    nodeClusters: new Map([["a", "group"], ["b", "group"]]),
  };
  const binding = bindGraphNodeDrag(svg, target, graph, layout);

  try {
    listeners.get("pointerdown")(pointerEvent(100, "pointerdown"));
    listeners.get("pointermove")(pointerEvent(200));
    const [[dragFrameId, dragFrame]] = frames;
    frames.delete(dragFrameId);
    dragFrame();
    listeners.get("pointerup")(pointerEvent(200, "pointerup"));
    const [[settleFrameId, settleFrame]] = frames;
    frames.delete(settleFrameId);
    settleFrame();
    const expectedCenter = (
      Number(nodes[0].dataset.x) + Number(nodes[1].dataset.x)
    ) / 2;
    assert.notEqual(haloAttributes.get("cx"), expectedCenter.toFixed(2));

    listeners.get("pointerdown")({ ...pointerEvent(850, "pointerdown"), clientY: 520 });

    assert.equal(frames.size, 0);
    assert.equal(haloAttributes.get("cx"), expectedCenter.toFixed(2));
  } finally {
    binding.destroy();
    if (previousAnimationFrame === undefined) delete globalThis.requestAnimationFrame;
    else globalThis.requestAnimationFrame = previousAnimationFrame;
    if (previousCancelFrame === undefined) delete globalThis.cancelAnimationFrame;
    else globalThis.cancelAnimationFrame = previousCancelFrame;
    if (previousMatchMedia === undefined) delete globalThis.matchMedia;
    else globalThis.matchMedia = previousMatchMedia;
  }
});
