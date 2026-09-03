import test from "node:test";
import assert from "node:assert/strict";

import { visibleGraphElements } from "../views/knowledge-graph-density.js";
import {
  denseDocumentGraph,
  largeGraphFixture,
  SMART_SETTINGS,
} from "./knowledge-graph-fixtures.mjs";

test("smart density preserves a complete graph below its capacity", () => {
  const entities = Array.from({ length: 5 }, (_, index) => ({
    id: `entity:${index}`,
    type: "entity",
    kind: "term",
    mentions: 1,
  }));
  const graph = {
    nodes: [{ id: "doc", type: "document" }, ...entities],
    edges: entities.map((entity, index) => ({
      id: `mention:${index}`,
      source: "doc",
      target: entity.id,
      type: "mentions",
      confidence: 0.5,
    })),
  };

  const visible = visibleGraphElements(
    graph, SMART_SETTINGS, "", { nodes: 48, edges: 100 },
  );

  assert.equal(visible.nodeIds.size, 6);
  assert.equal(visible.edgeIds.size, 5);
});

test("relation filtering retains matching endpoints during reduction", () => {
  const nodes = Array.from({ length: 49 }, (_, index) => ({
    id: `entity:${index}`,
    type: "entity",
    mentions: 1,
  }));
  const graph = {
    nodes,
    edges: [{
      id: "explicit",
      source: "entity:0",
      target: "entity:1",
      type: "depends_on",
    }],
  };
  const settings = { ...SMART_SETTINGS, relation: "explicit" };

  const visible = visibleGraphElements(
    graph, settings, "", { nodes: 48, edges: 100 },
  );

  assert.deepEqual([...visible.nodeIds].sort(), ["entity:0", "entity:1"]);
  assert.deepEqual([...visible.edgeIds], ["explicit"]);
});

test("relation filtering preserves all matching edges when endpoints fit", () => {
  const documents = Array.from({ length: 9 }, (_, index) => ({
    id: `doc:${index}`,
    type: "document",
  }));
  const entities = Array.from({ length: 40 }, (_, index) => ({
    id: `entity:${String(index).padStart(2, "0")}`,
    type: "entity",
    mentions: 1,
  }));
  const edges = Array.from({ length: 20 }, (_, index) => ({
    id: `explicit:${index}`,
    source: entities[index * 2].id,
    target: entities[index * 2 + 1].id,
    type: "depends_on",
  }));
  const settings = { ...SMART_SETTINGS, relation: "explicit" };

  const visible = visibleGraphElements(
    { nodes: [...documents, ...entities], edges },
    settings,
    "",
    { nodes: 48, edges: 100 },
  );

  assert.deepEqual([...visible.nodeIds].sort(), entities.map((node) => node.id));
  assert.deepEqual([...visible.edgeIds].sort(), edges.map((edge) => edge.id).sort());
});

test("relation filtering reserves capacity for complete matching edges", () => {
  const entities = Array.from({ length: 48 }, (_, index) => ({
    id: `entity:${String(index).padStart(2, "0")}`,
    type: "entity",
    mentions: 1,
  }));
  const documents = Array.from({ length: 48 }, (_, index) => ({
    id: `doc:${String(index).padStart(2, "0")}`,
    type: "document",
  }));
  const edges = Array.from({ length: 24 }, (_, index) => ({
    id: `explicit:${index}`,
    source: entities[index * 2].id,
    target: entities[index * 2 + 1].id,
    type: "depends_on",
  }));

  const visible = visibleGraphElements(
    { nodes: [...documents, ...entities], edges },
    { ...SMART_SETTINGS, relation: "explicit" },
    "",
    { nodes: 48, edges: 100 },
  );

  assert.equal(visible.nodeIds.size, 48);
  assert.equal(visible.edgeIds.size, 24);
  assert.ok(entities.every((node) => visible.nodeIds.has(node.id)));
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

  const visible = visibleGraphElements(
    graph, SMART_SETTINGS, "", { nodes: 2, edges: 1 },
  );

  assert.deepEqual([...visible.nodeIds].sort(), ["doc", "useful"]);
  assert.deepEqual([...visible.edgeIds].sort(), ["du"]);
});

test("smart density caps large graphs deterministically and keeps selected context", () => {
  const { documents, graph } = largeGraphFixture();
  const selectedId = "entity:89";
  const first = visibleGraphElements(
    graph, SMART_SETTINGS, selectedId, { nodes: 48, edges: 100 },
  );
  const second = visibleGraphElements(
    graph, SMART_SETTINGS, selectedId, { nodes: 48, edges: 100 },
  );

  assert.ok(first.nodeIds.size <= 48);
  assert.equal(first.nodeIds.has(selectedId), true);
  assert.ok(documents.every((node) => first.nodeIds.has(node.id)));
  assert.deepEqual([...first.nodeIds], [...second.nodeIds]);
  assert.deepEqual([...first.edgeIds], [...second.edgeIds]);
  graph.edges.filter((edge) => first.edgeIds.has(edge.id)).forEach((edge) => {
    assert.equal(first.nodeIds.has(edge.source), true);
    assert.equal(first.nodeIds.has(edge.target), true);
  });
});

test("smart density limits dense relationships without orphaning edge endpoints", () => {
  const graph = denseDocumentGraph(30);
  const visible = visibleGraphElements(
    graph, SMART_SETTINGS, "doc:29", { nodes: 48, edges: 40 },
  );

  assert.equal(visible.nodeIds.size, 30);
  assert.equal(visible.edgeIds.size, 40);
  graph.edges.filter((edge) => visible.edgeIds.has(edge.id)).forEach((edge) => {
    assert.equal(visible.nodeIds.has(edge.source), true);
    assert.equal(visible.nodeIds.has(edge.target), true);
  });
});

test("compact density reserves source documents ahead of high-degree entities", () => {
  const documents = Array.from({ length: 4 }, (_, index) => ({
    id: `doc:${index}`,
    type: "document",
  }));
  const entities = Array.from({ length: 60 }, (_, index) => ({
    id: `entity:${index}`,
    type: "entity",
    mentions: 3,
  }));
  const edges = entities.flatMap((entity, index) => [
    {
      id: `mention:${index}`,
      source: documents[index % documents.length].id,
      target: entity.id,
      type: "mentions",
      confidence: 0.9,
    },
    {
      id: `chain:${index}`,
      source: entity.id,
      target: entities[(index + 1) % entities.length].id,
      type: "similar_to",
      confidence: 0.9,
    },
  ]);
  const visible = visibleGraphElements(
    { nodes: [...documents, ...entities], edges },
    SMART_SETTINGS,
    "",
    { nodes: 48, edges: 100 },
  );

  assert.ok(documents.every((node) => visible.nodeIds.has(node.id)));
  assert.ok(visible.edgeIds.size > 0);
});

test("smart density keeps connected context when documents exceed the cap", () => {
  const documents = Array.from({ length: 80 }, (_, index) => ({
    id: `doc:${index}`,
    type: "document",
  }));
  const entities = Array.from({ length: 80 }, (_, index) => ({
    id: `entity:${index}`,
    type: "entity",
    mentions: 3,
  }));
  const edges = entities.map((entity, index) => ({
    id: `mention:${index}`,
    source: documents[index].id,
    target: entity.id,
    type: "mentions",
    confidence: 0.9,
  }));

  const visible = visibleGraphElements(
    { nodes: [...documents, ...entities], edges },
    SMART_SETTINGS,
    "",
    { nodes: 48, edges: 100 },
  );

  assert.equal(visible.nodeIds.size, 48);
  assert.ok(visible.edgeIds.size > 0);
  assert.ok(entities.some((node) => visible.nodeIds.has(node.id)));
});

test("disabling smart density preserves the complete filtered graph", () => {
  const graph = {
    nodes: Array.from({ length: 80 }, (_, index) => ({
      id: `doc:${index}`,
      type: "document",
    })),
    edges: Array.from({ length: 79 }, (_, index) => ({
      id: `edge:${index}`,
      source: `doc:${index}`,
      target: `doc:${index + 1}`,
      type: "depends_on",
    })),
  };
  const settings = { ...SMART_SETTINGS, smart: false };

  const visible = visibleGraphElements(
    graph, settings, "", { nodes: 5, edges: 5 },
  );

  assert.equal(visible.nodeIds.size, 80);
  assert.equal(visible.edgeIds.size, 79);
});

test("smart density keeps the selected node's low-confidence context", () => {
  const graph = {
    nodes: [
      { id: "doc", type: "document" },
      { id: "selected", type: "entity", mentions: 1 },
      ...Array.from({ length: 47 }, (_, index) => ({
        id: `noise:${index}`, type: "entity", mentions: 1,
      })),
    ],
    edges: [{
      id: "context",
      source: "doc",
      target: "selected",
      type: "mentions",
      confidence: 0.2,
    }],
  };
  const visible = visibleGraphElements(
    graph, SMART_SETTINGS, "selected", { nodes: 48, edges: 100 },
  );

  assert.deepEqual([...visible.nodeIds].sort(), ["doc", "selected"]);
  assert.deepEqual([...visible.edgeIds], ["context"]);
});
