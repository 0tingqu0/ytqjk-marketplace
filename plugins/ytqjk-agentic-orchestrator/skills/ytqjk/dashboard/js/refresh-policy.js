export function sameData(left, right) {
  return left === right || JSON.stringify(left) === JSON.stringify(right);
}

export async function loadKnowledgeGraph(api, currentRevision = "") {
  if (currentRevision) {
    const current = await api.knowledgeGraphRevision();
    if (current.revision === currentRevision) {
      return { changed: false, revision: currentRevision };
    }
  }
  const response = await api.knowledgeGraph();
  return {
    changed: true,
    revision: response.revision,
    graph: response.graph,
  };
}

export function shouldAutoRefresh(hidden) {
  return !hidden;
}
