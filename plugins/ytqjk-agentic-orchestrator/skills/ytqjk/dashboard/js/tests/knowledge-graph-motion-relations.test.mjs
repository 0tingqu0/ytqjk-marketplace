import assert from "node:assert/strict";
import test from "node:test";

import { bindKnowledgeGraphMotion } from "../views/knowledge-graph-motion.js";

function classes(...initial) {
  const values = new Set(initial);
  return {
    add(...names) { names.forEach((name) => values.add(name)); },
    contains(name) { return values.has(name); },
    remove(...names) { names.forEach((name) => values.delete(name)); },
  };
}

function graphElement(dataset = {}, ...initialClasses) {
  return {
    classList: classes(...initialClasses),
    dataset,
    style: { setProperty() {} },
  };
}

test("node relations restore focus after hover and index both graph types", () => {
  const nodes = ["a", "b", "c"].map((node) => graphElement({ node }));
  const semanticLine = graphElement();
  const semanticEdge = graphElement({ source: "a", target: "b" });
  semanticEdge.matches = () => false;
  semanticEdge.querySelector = () => semanticLine;
  const libraryEdge = graphElement(
    { source: "a", target: "c" },
    "graph-edge",
  );
  libraryEdge.matches = (selector) => selector === ".graph-edge";
  const target = {
    classList: classes(),
    style: { setProperty() {} },
    getBoundingClientRect() {
      return { left: 0, top: 0, width: 100, height: 100 };
    },
    querySelectorAll(selector) {
      if (selector === ".graph-node-link") return nodes;
      return [semanticEdge, libraryEdge];
    },
  };

  const binding = bindKnowledgeGraphMotion(target);
  nodes[0].onfocus();
  assert.equal(nodes[0].classList.contains("is-active"), true);
  assert.equal(nodes[2].classList.contains("is-related"), true);
  assert.equal(libraryEdge.classList.contains("is-related"), true);

  nodes[1].onpointerenter();
  assert.equal(nodes[1].classList.contains("is-active"), true);
  assert.equal(libraryEdge.classList.contains("is-related"), false);
  assert.equal(semanticLine.classList.contains("is-related"), true);

  nodes[1].onpointerleave();
  assert.equal(nodes[0].classList.contains("is-active"), true);
  assert.equal(nodes[2].classList.contains("is-related"), true);
  assert.equal(libraryEdge.classList.contains("is-related"), true);

  nodes[0].onblur();
  assert.equal(target.classList.contains("has-active-node"), false);
  binding.destroy();
});
