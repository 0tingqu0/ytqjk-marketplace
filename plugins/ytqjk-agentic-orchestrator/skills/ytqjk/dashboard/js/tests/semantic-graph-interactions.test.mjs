import assert from "node:assert/strict";
import test from "node:test";

import { bindKnowledgeGraphMotion } from "../views/knowledge-graph-motion.js";
import { bindGraphViewport } from "../views/knowledge-graph-viewport.js";
import { bindSemanticNodeSelection } from "../views/semantic-graph-render.js";

function coordinateSvg(node = null) {
  const attributes = new Map([["viewBox", "0 0 900 570"]]);
  const listeners = new Map();
  return {
    attributes,
    listeners,
    createSVGPoint() {
      return {
        x: 0,
        y: 0,
        matrixTransform() { return { x: this.x, y: this.y }; },
      };
    },
    getScreenCTM() { return { inverse() { return {}; } }; },
    getAttribute(name) { return attributes.get(name); },
    setAttribute(name, value) { attributes.set(name, value); },
    getBoundingClientRect() {
      return { left: 0, top: 0, width: 900, height: 570 };
    },
    querySelectorAll() { return node ? [node] : []; },
    addEventListener(type, listener) { listeners.set(type, listener); },
  };
}

test("semantic nodes accept assistive-technology synthetic clicks", () => {
  const element = {
    dataset: { node: "entity:one", x: "20", y: "30" },
    closest() { return this; },
    querySelector() { return { getAttribute() { return "22"; } }; },
  };
  const svg = coordinateSvg(element);
  let selected = null;
  bindSemanticNodeSelection(
    svg,
    { nodes: [{ id: "entity:one" }] },
    (node) => { selected = node.id; },
  );

  svg.onclick({ detail: 0, clientX: 0, clientY: 0, target: element });

  assert.equal(selected, "entity:one");
});

test("handled node arrows do not also pan the graph viewport", () => {
  const previousObserver = globalThis.ResizeObserver;
  const previousCancelFrame = globalThis.cancelAnimationFrame;
  globalThis.ResizeObserver = class {
    observe() {}
    disconnect() {}
  };
  globalThis.cancelAnimationFrame = () => {};
  const svg = coordinateSvg();
  const target = {
    dataset: {},
    classList: { toggle() {}, add() {}, remove() {} },
  };

  try {
    const viewport = bindGraphViewport(svg, target);
    const before = svg.getAttribute("viewBox");
    let prevented = false;
    svg.listeners.get("keydown")({
      key: "ArrowRight",
      defaultPrevented: true,
      preventDefault() { prevented = true; },
    });

    assert.equal(svg.getAttribute("viewBox"), before);
    assert.equal(prevented, false);
    viewport.destroy();
  } finally {
    if (previousObserver === undefined) delete globalThis.ResizeObserver;
    else globalThis.ResizeObserver = previousObserver;
    if (previousCancelFrame === undefined) delete globalThis.cancelAnimationFrame;
    else globalThis.cancelAnimationFrame = previousCancelFrame;
  }
});

test("pointer light batches same-frame moves and uses the latest coordinates", () => {
  const previousAnimationFrame = globalThis.requestAnimationFrame;
  const frames = [];
  const writes = [];
  let rectangleReads = 0;
  globalThis.requestAnimationFrame = (callback) => {
    frames.push(callback);
    return frames.length;
  };
  const target = {
    classList: { add() {}, remove() {} },
    style: {
      setProperty(name, value) { writes.push([name, value]); },
    },
    getBoundingClientRect() {
      rectangleReads += 1;
      return { left: 10, top: 20, width: 100, height: 100 };
    },
    querySelectorAll() { return []; },
  };

  try {
    bindKnowledgeGraphMotion(target);
    for (let index = 0; index < 40; index += 1) {
      target.onpointermove({ clientX: 10 + index, clientY: 20 + index * 2 });
    }

    assert.equal(frames.length, 1);
    assert.equal(rectangleReads, 0);
    assert.deepEqual(writes, []);

    frames.shift()(0);

    assert.equal(rectangleReads, 1);
    assert.deepEqual(writes, [
      ["--graph-cursor-x", "39.00%"],
      ["--graph-cursor-y", "78.00%"],
    ]);
  } finally {
    if (previousAnimationFrame === undefined) {
      delete globalThis.requestAnimationFrame;
    } else {
      globalThis.requestAnimationFrame = previousAnimationFrame;
    }
  }
});

test("pointer light stays disabled when reduced motion is requested", () => {
  const previousMatchMedia = globalThis.matchMedia;
  globalThis.matchMedia = (query) => ({
    matches: query.includes("prefers-reduced-motion"),
  });
  const target = {
    classList: { add() {}, remove() {} },
    style: { setProperty() {} },
    querySelectorAll() { return []; },
  };

  try {
    bindKnowledgeGraphMotion(target);
    assert.equal(target.onpointermove, null);
    assert.equal(typeof target.onpointerleave, "function");
  } finally {
    if (previousMatchMedia === undefined) delete globalThis.matchMedia;
    else globalThis.matchMedia = previousMatchMedia;
  }
});

test("motion cleanup cancels pending pointer work and clears graph state", () => {
  const previousAnimationFrame = globalThis.requestAnimationFrame;
  const previousCancelFrame = globalThis.cancelAnimationFrame;
  const frames = new Map();
  const classes = new Set(["has-active-node"]);
  globalThis.requestAnimationFrame = (callback) => {
    frames.set(1, callback);
    return 1;
  };
  globalThis.cancelAnimationFrame = (identifier) => frames.delete(identifier);
  const target = {
    classList: {
      add(name) { classes.add(name); },
      contains(name) { return classes.has(name); },
      remove(...names) { names.forEach((name) => classes.delete(name)); },
    },
    style: { setProperty() {} },
    getBoundingClientRect() {
      return { left: 0, top: 0, width: 100, height: 100 };
    },
    querySelectorAll() { return []; },
  };

  try {
    const binding = bindKnowledgeGraphMotion(target);
    target.onpointermove({ clientX: 20, clientY: 30 });
    assert.equal(frames.size, 1);
    binding.destroy();
    assert.equal(frames.size, 0);
    assert.equal(target.onpointermove, null);
    assert.equal(target.onpointerleave, null);
    assert.equal(classes.has("has-active-node"), false);
    assert.equal(classes.has("has-pointer-light"), false);
  } finally {
    if (previousAnimationFrame === undefined) {
      delete globalThis.requestAnimationFrame;
    } else globalThis.requestAnimationFrame = previousAnimationFrame;
    if (previousCancelFrame === undefined) {
      delete globalThis.cancelAnimationFrame;
    } else globalThis.cancelAnimationFrame = previousCancelFrame;
  }
});

test("viewport panning batches pointer bursts into one frame", () => {
  const previousAnimationFrame = globalThis.requestAnimationFrame;
  const previousCancelFrame = globalThis.cancelAnimationFrame;
  const previousObserver = globalThis.ResizeObserver;
  const frames = new Map();
  let nextFrame = 1;
  globalThis.requestAnimationFrame = (callback) => {
    const identifier = nextFrame;
    nextFrame += 1;
    frames.set(identifier, callback);
    return identifier;
  };
  globalThis.cancelAnimationFrame = (identifier) => frames.delete(identifier);
  globalThis.ResizeObserver = class {
    observe() {}
    disconnect() {}
  };
  const svg = coordinateSvg();
  const originalSetAttribute = svg.setAttribute.bind(svg);
  let viewBoxWrites = 0;
  svg.setAttribute = (name, value) => {
    if (name === "viewBox") viewBoxWrites += 1;
    originalSetAttribute(name, value);
  };
  svg.focus = () => {};
  svg.setPointerCapture = () => {};
  svg.hasPointerCapture = () => true;
  let releasedPointer = null;
  svg.releasePointerCapture = (pointerId) => { releasedPointer = pointerId; };
  const classes = new Set();
  const target = {
    dataset: {},
    classList: {
      toggle() {},
      add(name) { classes.add(name); },
      remove(name) { classes.delete(name); },
    },
  };
  let changeCalls = 0;
  const viewport = bindGraphViewport(svg, target, () => { changeCalls += 1; });
  let destroyed = false;

  try {
    const initialWrites = viewBoxWrites;
    const initialChanges = changeCalls;
    svg.listeners.get("pointerdown")({
      button: 0,
      clientX: 100,
      clientY: 100,
      pointerId: 7,
      target: { closest() { return null; } },
    });
    for (let index = 0; index < 40; index += 1) {
      svg.listeners.get("pointermove")({
        clientX: 120 + index,
        clientY: 130 + index,
        pointerId: 7,
      });
    }

    assert.equal(frames.size, 1);
    assert.equal(viewBoxWrites, initialWrites);
    const [[identifier, renderFrame]] = frames;
    frames.delete(identifier);
    renderFrame(16.67);
    assert.equal(viewBoxWrites, initialWrites + 1);
    assert.equal(
      changeCalls,
      initialChanges,
      "panning must not repeat zoom-only change notifications",
    );
    viewport.destroy();
    destroyed = true;
    assert.equal(classes.has("is-panning"), false);
    assert.equal(releasedPointer, 7);
  } finally {
    if (!destroyed) viewport.destroy();
    if (previousAnimationFrame === undefined) {
      delete globalThis.requestAnimationFrame;
    } else globalThis.requestAnimationFrame = previousAnimationFrame;
    if (previousCancelFrame === undefined) {
      delete globalThis.cancelAnimationFrame;
    } else globalThis.cancelAnimationFrame = previousCancelFrame;
    if (previousObserver === undefined) delete globalThis.ResizeObserver;
    else globalThis.ResizeObserver = previousObserver;
  }
});
