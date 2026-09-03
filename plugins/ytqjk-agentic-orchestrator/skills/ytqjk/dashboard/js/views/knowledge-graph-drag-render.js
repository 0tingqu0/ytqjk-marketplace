function edgeCache(svg) {
  const labels = new Map([...svg.querySelectorAll(".semantic-edge-label")].map(
    (element) => [element.dataset.edge, element],
  ));
  return new Map([...svg.querySelectorAll(".semantic-edge-link")].map(
    (element) => [element.dataset.edge, {
      element,
      label: labels.get(element.dataset.edge),
      lines: null,
    }],
  ));
}

export function createGraphDragRenderer(svg) {
  const nodeElements = new Map([...svg.querySelectorAll("[data-node]")].map(
    (element) => [element.dataset.node, element],
  ));
  const edgeElements = edgeCache(svg);
  let activeNodes = [];
  let activeEdges = [];

  return {
    nodeElements,
    prepare(movable, edges) {
      activeNodes = [...movable].map((nodeId) => (
        [nodeId, nodeElements.get(nodeId)]
      )).filter(([, element]) => element);
      activeEdges = edges.map((edge) => {
        const cached = edgeElements.get(edge.id);
        if (cached && !cached.lines) {
          cached.lines = [...cached.element.querySelectorAll("line")];
        }
        return [edge, cached];
      }).filter(([, cached]) => cached);
    },
    render(points) {
      activeNodes.forEach(([nodeId, element]) => {
        const point = points.get(nodeId);
        if (!point) return;
        const x = point.x.toFixed(2);
        const y = point.y.toFixed(2);
        element.setAttribute("transform", `translate(${x} ${y})`);
        element.dataset.x = x;
        element.dataset.y = y;
      });
      activeEdges.forEach(([edge, cached]) => {
        const source = points.get(edge.source);
        const target = points.get(edge.target);
        if (!source || !target) return;
        const x1 = source.x.toFixed(2);
        const y1 = source.y.toFixed(2);
        const x2 = target.x.toFixed(2);
        const y2 = target.y.toFixed(2);
        cached.lines.forEach((line) => {
          line.setAttribute("x1", x1);
          line.setAttribute("y1", y1);
          line.setAttribute("x2", x2);
          line.setAttribute("y2", y2);
        });
        cached.label?.setAttribute(
          "x", ((source.x + target.x) / 2).toFixed(2),
        );
        cached.label?.setAttribute(
          "y", ((source.y + target.y) / 2 - 5).toFixed(2),
        );
      });
    },
  };
}
