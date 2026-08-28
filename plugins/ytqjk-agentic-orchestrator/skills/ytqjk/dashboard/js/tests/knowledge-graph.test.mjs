import assert from "node:assert/strict";
import test from "node:test";

import {
  loadKnowledgeGraph,
  sameData,
  shouldAutoRefresh,
} from "../refresh-policy.js";
import {
  graphNeighborhood,
  visibleGraphElements,
} from "../views/knowledge-graph-explorer.js";
import { layoutKnowledgeGraph } from "../views/knowledge-graph-layout.js";
import { zoomViewBox } from "../views/knowledge-graph-viewport.js";

test("sameData compares graph snapshots structurally", () => {
  assert.equal(sameData({ revision: "one" }, { revision: "one" }), true);
  assert.equal(sameData({ revision: "one" }, { revision: "two" }), false);
});

test("loadKnowledgeGraph skips an unchanged graph", async () => {
  let revisionCalls = 0;
  let graphCalls = 0;
  const api = {
    async knowledgeGraphRevision() {
      revisionCalls += 1;
      return { revision: "same" };
    },
    async knowledgeGraph() {
      graphCalls += 1;
      return { revision: "new", graph: { nodes: [], edges: [] } };
    },
  };

  const result = await loadKnowledgeGraph(api, "same");

  assert.deepEqual(result, { changed: false, revision: "same" });
  assert.equal(revisionCalls, 1);
  assert.equal(graphCalls, 0);
});

test("loadKnowledgeGraph fetches a changed graph", async () => {
  const graph = { nodes: [{ id: "document" }], edges: [] };
  let graphCalls = 0;
  const api = {
    async knowledgeGraphRevision() {
      return { revision: "new" };
    },
    async knowledgeGraph() {
      graphCalls += 1;
      return { revision: "new", graph };
    },
  };

  const result = await loadKnowledgeGraph(api, "old");

  assert.deepEqual(result, { changed: true, revision: "new", graph });
  assert.equal(graphCalls, 1);
});

test("automatic refresh runs only while the page is visible", () => {
  assert.equal(shouldAutoRefresh(true), false);
  assert.equal(shouldAutoRefresh(false), true);
});

test("zoomViewBox remains finite and bounded", () => {
  const base = { x: 0, y: 0, width: 900, height: 570 };
  const anchor = { x: 450, y: 285 };
  let viewBox = base;

  for (let index = 0; index < 30; index += 1) {
    viewBox = zoomViewBox(viewBox, 0.8, anchor, base);
  }
  assert.ok(Math.abs(viewBox.width - base.width / 3) < 1e-9);
  assert.ok(Math.abs(viewBox.height - base.height / 3) < 1e-9);

  for (let index = 0; index < 60; index += 1) {
    viewBox = zoomViewBox(viewBox, 1.25, anchor, base);
  }
  assert.ok(Math.abs(viewBox.width - base.width * 1.5) < 1e-9);
  assert.ok(Math.abs(viewBox.height - base.height * 1.5) < 1e-9);
  assert.ok(Object.values(viewBox).every(Number.isFinite));
});

test("layoutKnowledgeGraph produces finite node coordinates", () => {
  const graph = {
    nodes: [
      { id: "doc:a", type: "document", path: "docs/a.md" },
      { id: "entity:b", type: "entity" },
    ],
    edges: [
      {
        id: "mentions:a:b",
        source: "doc:a",
        target: "entity:b",
        type: "mentions",
      },
    ],
  };

  const result = layoutKnowledgeGraph(graph);
  const coordinates = [...result.positions.values()].flatMap(Object.values);

  assert.equal(result.positions.size, graph.nodes.length);
  assert.ok(coordinates.every(Number.isFinite));
});

test("graphNeighborhood follows connected nodes to the requested depth", () => {
  const graph = {
    nodes: ["a", "b", "c", "d"].map((id) => ({ id })),
    edges: [
      { id: "ab", source: "a", target: "b" },
      { id: "bc", source: "b", target: "c" },
    ],
  };

  const oneHop = graphNeighborhood(graph, "a", 1);
  const twoHops = graphNeighborhood(graph, "a", 2);

  assert.deepEqual([...oneHop.nodeIds].sort(), ["a", "b"]);
  assert.deepEqual([...oneHop.edgeIds].sort(), ["ab"]);
  assert.deepEqual([...twoHops.nodeIds].sort(), ["a", "b", "c"]);
  assert.deepEqual([...twoHops.edgeIds].sort(), ["ab", "bc"]);
  assert.equal(twoHops.nodeIds.has("d"), false);
});

test("smart density hides low-value leaf entities", () => {
  const graph = {
    nodes: [
      { id: "doc", type: "document", kind: "document" },
      { id: "useful", type: "entity", kind: "concept", mentions: 4 },
      { id: "noise", type: "entity", kind: "term", mentions: 1 },
    ],
    edges: [
      { id: "du", source: "doc", target: "useful", type: "mentions" },
      { id: "dn", source: "doc", target: "noise", type: "mentions" },
    ],
  };
  const settings = {
    local: false,
    depth: 1,
    documents: true,
    entities: true,
    smart: true,
    relation: "all",
  };

  const visible = visibleGraphElements(graph, settings, "");

  assert.deepEqual([...visible.nodeIds].sort(), ["doc", "useful"]);
  assert.deepEqual([...visible.edgeIds].sort(), ["du"]);
});
