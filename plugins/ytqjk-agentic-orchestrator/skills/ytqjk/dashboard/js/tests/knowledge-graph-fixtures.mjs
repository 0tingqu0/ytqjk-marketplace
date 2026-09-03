export const SMART_SETTINGS = Object.freeze({
  local: false,
  depth: 1,
  documents: true,
  entities: true,
  smart: true,
  relation: "all",
});

export function largeGraphFixture() {
  const documents = Array.from({ length: 30 }, (_, index) => ({
    id: `doc:${String(index).padStart(2, "0")}`,
    type: "document",
  }));
  const entities = Array.from({ length: 90 }, (_, index) => ({
    id: `entity:${String(index).padStart(2, "0")}`,
    type: "entity",
    mentions: (index % 6) + 1,
    document_count: 3,
  }));
  const edges = entities.map((entity, index) => ({
    id: `edge:${String(index).padStart(2, "0")}`,
    source: documents[index % documents.length].id,
    target: entity.id,
    type: "mentions",
    confidence: 0.9,
    weight: 2,
  }));
  return { documents, graph: { nodes: [...documents, ...entities], edges } };
}

export function denseDocumentGraph(count) {
  const nodes = Array.from({ length: count }, (_, index) => ({
    id: `doc:${index}`,
    type: "document",
  }));
  const edges = [];
  nodes.forEach((source, sourceIndex) => {
    nodes.slice(sourceIndex + 1).forEach((target) => edges.push({
      id: `${source.id}:${target.id}`,
      source: source.id,
      target: target.id,
      type: "depends_on",
      confidence: 0.8,
    }));
  });
  return { nodes, edges };
}
