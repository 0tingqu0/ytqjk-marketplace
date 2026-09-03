import assert from "node:assert/strict";
import test from "node:test";

import {
  graphPointFromEvent,
  nearestGraphElement,
  nearestGraphNode,
  watchGraphNodeHitTargets,
} from "../views/knowledge-graph-hit-targets.js";

test("graph points use the SVG screen matrix through letterboxed viewports", () => {
  const svg = {
    createSVGPoint() {
      return {
        x: 0,
        y: 0,
        matrixTransform() {
          return { x: (this.x - 12) / 0.4, y: (this.y - 63) / 0.4 };
        },
      };
    },
    getScreenCTM() { return { inverse() { return {}; } }; },
  };

  assert.deepEqual(
    graphPointFromEvent(svg, { clientX: 92, clientY: 103 }),
    { x: 200, y: 100 },
  );
});

test("nearest graph node partitions overlapping hit areas", () => {
  const points = [
    { id: "left", x: 10, y: 10 },
    { id: "right", x: 30, y: 10 },
  ];
  assert.equal(nearestGraphNode(points, 12, 10).id, "left");
  assert.equal(nearestGraphNode(points, 28, 10).id, "right");
  assert.equal(nearestGraphNode([
    { id: "behind", x: 20, y: 10 },
    { id: "painted-last", x: 20, y: 10 },
  ], 20, 10).id, "painted-last");
});

test("graph element hit-testing selects the nearest overlapping node", () => {
  const element = (id, x) => ({
    dataset: { node: id, x: String(x), y: "10" },
    querySelector() {
      return { getAttribute() { return "22"; } };
    },
  });
  const left = element("left", 10);
  const right = element("right", 30);
  const svg = {
    createSVGPoint() {
      return {
        x: 0,
        y: 0,
        matrixTransform() { return { x: this.x, y: this.y }; },
      };
    },
    getScreenCTM() { return { inverse() { return {}; } }; },
    querySelectorAll() { return [left, right]; },
  };

  assert.equal(nearestGraphElement(
    svg, { clientX: 12, clientY: 10 }, ".node",
  ), left);
  assert.equal(nearestGraphElement(
    svg, { clientX: 70, clientY: 10 }, ".node",
  ), null);
});

test("graph hit targets stay 44 CSS pixels and release their observer", () => {
  const previousObserver = globalThis.ResizeObserver;
  const attributes = new Map();
  let disconnected = false;
  let observed = null;
  globalThis.ResizeObserver = class {
    constructor(callback) { this.callback = callback; }
    observe(target) { observed = target; }
    disconnect() { disconnected = true; }
  };
  const hit = {
    setAttribute(name, value) { attributes.set(name, value); },
  };
  const svg = {
    getBoundingClientRect() { return { width: 328, height: 240 }; },
    getAttribute() { return "0 0 820 600"; },
    querySelectorAll() { return [hit]; },
  };

  try {
    const stop = watchGraphNodeHitTargets(svg);
    assert.equal(attributes.get("r"), "55.00");
    assert.equal(observed, svg);
    stop();
    assert.equal(disconnected, true);
  } finally {
    if (previousObserver === undefined) delete globalThis.ResizeObserver;
    else globalThis.ResizeObserver = previousObserver;
  }
});
