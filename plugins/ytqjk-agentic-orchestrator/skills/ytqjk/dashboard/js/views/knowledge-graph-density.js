const DENSITY_LIMITS = Object.freeze({
  desktop: Object.freeze({ nodes: 72, edges: 180 }),
  compact: Object.freeze({ nodes: 48, edges: 100 }),
});

function adjacency(graph) {
  const neighbors = new Map((graph.nodes || []).map((node) => [node.id, []]));
  (graph.edges || []).forEach((edge) => {
    if (!neighbors.has(edge.source) || !neighbors.has(edge.target)) return;
    neighbors.get(edge.source).push({ nodeId: edge.target, edgeId: edge.id });
    neighbors.get(edge.target).push({ nodeId: edge.source, edgeId: edge.id });
  });
  return neighbors;
}

export function graphNeighborhood(graph, selectedId, depth = 1) {
  const neighbors = adjacency(graph);
  const nodeIds = new Set();
  const edgeIds = new Set();
  if (!neighbors.has(selectedId)) return { nodeIds, edgeIds };
  nodeIds.add(selectedId);
  let frontier = new Set([selectedId]);
  for (let level = 0; level < depth; level += 1) {
    const next = new Set();
    frontier.forEach((nodeId) => {
      neighbors.get(nodeId).forEach((neighbor) => {
        edgeIds.add(neighbor.edgeId);
        if (!nodeIds.has(neighbor.nodeId)) next.add(neighbor.nodeId);
        nodeIds.add(neighbor.nodeId);
      });
    });
    frontier = next;
    if (!frontier.size) break;
  }
  return { nodeIds, edgeIds };
}

export function graphDensityLimits(compact = null) {
  const isCompact = compact ?? (
    typeof window !== "undefined"
    && window.matchMedia?.("(max-width: 900px)").matches
  );
  return isCompact ? DENSITY_LIMITS.compact : DENSITY_LIMITS.desktop;
}

function relationMatches(edge, relation) {
  if (relation === "all") return true;
  if (relation === "explicit") {
    return edge.type !== "mentions" && edge.type !== "similar_to";
  }
  return edge.type === relation;
}

function importantEdge(edge) {
  const confidence = Number(edge.confidence);
  if (!Number.isFinite(confidence)) return true;
  if (edge.type === "mentions") return confidence >= 0.78 || edge.weight > 1;
  if (edge.type === "similar_to") return confidence >= 0.55;
  return true;
}

function nodeDegree(nodes, edges) {
  const degree = new Map(nodes.map((node) => [node.id, 0]));
  edges.forEach((edge) => {
    degree.set(edge.source, (degree.get(edge.source) || 0) + 1);
    degree.set(edge.target, (degree.get(edge.target) || 0) + 1);
  });
  return degree;
}

function importantNode(node, degree) {
  return node.type === "document"
    || Number(node.mentions || 0) >= 2
    || Number(node.document_count || 0) >= 2
    || degree > 1;
}

function byScore(score) {
  return (left, right) => {
    const difference = score(right) - score(left);
    if (difference) return difference;
    return String(left.id).localeCompare(String(right.id));
  };
}

function selectedNeighbors(edges, selectedId) {
  const neighbors = new Set();
  edges.forEach((edge) => {
    if (edge.source === selectedId) neighbors.add(edge.target);
    if (edge.target === selectedId) neighbors.add(edge.source);
  });
  return neighbors;
}

function edgeScore(edge, degree, selectedId) {
  return (
    (edge.source === selectedId || edge.target === selectedId ? 1e9 : 0)
    + (edge.type !== "mentions" && edge.type !== "similar_to" ? 1e6 : 0)
    + Number(edge.confidence || 0) * 10000
    + Number(edge.weight || 0) * 1000
    + (degree.get(edge.source) || 0) + (degree.get(edge.target) || 0)
  );
}

function connectedSeed(nodes, edges, degree, selectedId, limit, fillFromEdges) {
  const available = new Set(nodes.map((node) => node.id));
  const selected = selectedId && available.has(selectedId) ? selectedId : "";
  const nodeIds = new Set(selected ? [selected] : []);
  const seedLimit = fillFromEdges
    ? limit
    : Math.min(limit, Math.max(2, Math.floor(limit * 0.75)));
  const rankedEdges = [...edges].sort(byScore(
    (edge) => edgeScore(edge, degree, selectedId),
  ));
  for (const edge of rankedEdges) {
    const missing = [edge.source, edge.target].filter(
      (nodeId) => !nodeIds.has(nodeId),
    );
    if (nodeIds.size + missing.length > seedLimit) continue;
    missing.forEach((nodeId) => nodeIds.add(nodeId));
    if (nodeIds.size >= seedLimit) break;
  }
  return nodeIds;
}

function limitNodes(nodes, edges, degree, selectedId, limit, fillFromEdges) {
  if (nodes.length <= limit) return new Set(nodes.map((node) => node.id));
  const neighbors = selectedNeighbors(edges, selectedId);
  const score = (node) => (
    (node.id === selectedId ? 1e12 : 0)
    + (neighbors.has(node.id) ? 1e10 : 0)
    + (node.type === "document" ? 1e8 : 0)
    + (degree.get(node.id) || 0) * 1000
    + Number(node.document_count || 0) * 600
    + Number(node.mentions || 0) * 400
    + Number(node.confidence || 0) * 100
  );
  const nodeIds = connectedSeed(
    nodes, edges, degree, selectedId, limit, fillFromEdges,
  );
  if (nodeIds.size >= limit) return nodeIds;
  [...nodes].sort(byScore(score)).some((node) => {
    nodeIds.add(node.id);
    return nodeIds.size >= limit;
  });
  return nodeIds;
}

function limitEdges(edges, degree, selectedId, limit) {
  if (edges.length <= limit) return edges;
  return [...edges].sort(byScore(
    (edge) => edgeScore(edge, degree, selectedId),
  )).slice(0, limit);
}

function removeDisconnectedNodes(graph, nodeIds, edgeIds, selectedId, all) {
  const connected = new Set(selectedId && nodeIds.has(selectedId) ? [selectedId] : []);
  (graph.edges || []).forEach((edge) => {
    if (!edgeIds.has(edge.id)) return;
    connected.add(edge.source);
    connected.add(edge.target);
  });
  (graph.nodes || []).forEach((node) => {
    if (!nodeIds.has(node.id)) return;
    if (!connected.has(node.id) && (all || node.type !== "document")) {
      nodeIds.delete(node.id);
    }
  });
}

export function visibleGraphElements(
  graph, settings, selectedId, limits = graphDensityLimits(),
) {
  const nodes = graph.nodes || [];
  const edges = graph.edges || [];
  const localIds = settings.local
    ? graphNeighborhood(graph, selectedId, settings.depth).nodeIds
    : new Set(nodes.map((node) => node.id));
  let candidates = nodes.filter((node) => (
    localIds.has(node.id)
    && (node.type === "document" ? settings.documents : settings.entities)
  ));
  let candidateIds = new Set(candidates.map((node) => node.id));
  let candidateEdges = edges.filter((edge) => (
    candidateIds.has(edge.source)
    && candidateIds.has(edge.target)
    && relationMatches(edge, settings.relation)
  ));
  const shouldReduce = settings.smart && (
    candidates.length > limits.nodes || candidateEdges.length > limits.edges
  );
  if (shouldReduce) {
    candidateEdges = candidateEdges.filter((edge) => (
      importantEdge(edge)
      || edge.source === selectedId
      || edge.target === selectedId
    ));
  }
  const relationEndpoints = new Set();
  if (shouldReduce && settings.relation !== "all") {
    candidateEdges.forEach((edge) => {
      relationEndpoints.add(edge.source);
      relationEndpoints.add(edge.target);
    });
  }
  const degree = nodeDegree(candidates, candidateEdges);
  if (shouldReduce) {
    const neighbors = selectedNeighbors(candidateEdges, selectedId);
    candidates = candidates.filter((node) => (
      node.id === selectedId
      || neighbors.has(node.id)
      || relationEndpoints.has(node.id)
      || importantNode(node, degree.get(node.id) || 0)
    ));
    candidateIds = new Set(candidates.map((node) => node.id));
    candidateEdges = candidateEdges.filter((edge) => (
      candidateIds.has(edge.source) && candidateIds.has(edge.target)
    ));
  }
  const nodeIds = shouldReduce
    ? limitNodes(
      candidates,
      candidateEdges,
      degree,
      selectedId,
      limits.nodes,
      settings.relation !== "all",
    )
    : candidateIds;
  const visibleEdges = candidateEdges.filter((edge) => (
    nodeIds.has(edge.source) && nodeIds.has(edge.target)
  ));
  const limitedEdges = shouldReduce
    ? limitEdges(visibleEdges, degree, selectedId, limits.edges)
    : visibleEdges;
  const edgeIds = new Set(limitedEdges.map((edge) => edge.id));
  if (shouldReduce) {
    removeDisconnectedNodes(graph, nodeIds, edgeIds, selectedId, false);
  }
  if (settings.relation !== "all") {
    removeDisconnectedNodes(graph, nodeIds, edgeIds, selectedId, true);
  }
  return { nodeIds, edgeIds, limits };
}
