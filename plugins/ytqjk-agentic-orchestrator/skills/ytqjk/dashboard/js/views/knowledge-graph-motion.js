function relationIndex(target) {
  const nodes = new Map([...target.querySelectorAll(".graph-node-link")].map(
    (link, index) => {
      link.style.setProperty("--graph-order", String(index));
      return [link.dataset.node, link];
    },
  ));
  const edges = new Map();
  target.querySelectorAll(
    ".semantic-edge-link, .graph-edge[data-source][data-target]",
  ).forEach((edge) => {
    const line = edge.matches?.(".graph-edge")
      ? edge
      : edge.querySelector(".graph-edge");
    [edge.dataset.source, edge.dataset.target].forEach((nodeId) => {
      if (!edges.has(nodeId)) edges.set(nodeId, []);
      edges.get(nodeId).push({ edge, line });
    });
  });
  return { edges, nodes };
}

function bindNodeRelations(target) {
  const index = relationIndex(target);
  const highlighted = new Set();
  let focusedId = "";
  let hoveredId = "";

  function clearHighlights() {
    target.classList.remove("has-active-node");
    highlighted.forEach((element) => {
      element.classList.remove("is-active", "is-related");
    });
    highlighted.clear();
  }

  function activate(nodeId) {
    clearHighlights();
    const related = new Set([nodeId]);
    (index.edges.get(nodeId) || []).forEach(({ edge, line }) => {
      if (edge.classList.contains("is-filtered-out")) return;
      edge.classList.add("is-related");
      line?.classList.add("is-related");
      highlighted.add(edge);
      if (line) highlighted.add(line);
      related.add(edge.dataset.source);
      related.add(edge.dataset.target);
    });
    related.forEach((relatedId) => {
      const link = index.nodes.get(relatedId);
      if (!link || link.classList.contains("is-filtered-out")) return;
      link.classList.add("is-related");
      if (relatedId === nodeId) link.classList.add("is-active");
      highlighted.add(link);
    });
    target.classList.add("has-active-node");
  }

  function refresh() {
    const activeId = hoveredId || focusedId;
    if (activeId) activate(activeId);
    else clearHighlights();
  }

  function clearHover() {
    hoveredId = "";
    refresh();
  }

  index.nodes.forEach((link) => {
    const nodeId = link.dataset.node;
    link.onpointerenter = () => {
      hoveredId = nodeId;
      refresh();
    };
    link.onpointerleave = () => {
      if (hoveredId === nodeId) hoveredId = "";
      refresh();
    };
    link.onfocus = () => {
      focusedId = nodeId;
      refresh();
    };
    link.onblur = () => {
      if (focusedId === nodeId) focusedId = "";
      refresh();
    };
  });
  return {
    clearHover,
    destroy() {
      focusedId = "";
      hoveredId = "";
      clearHighlights();
      index.nodes.forEach((link) => {
        link.onpointerenter = null;
        link.onpointerleave = null;
        link.onfocus = null;
        link.onblur = null;
      });
    },
  };
}

function pointerLightAllowed() {
  const reduced = globalThis.matchMedia?.(
    "(prefers-reduced-motion: reduce)",
  ).matches;
  const finePointer = globalThis.matchMedia?.(
    "(hover: hover) and (pointer: fine)",
  ).matches;
  return !reduced && finePointer !== false;
}

function bindPointerLight(target, clearRelations) {
  let frame = 0;
  let pointer = null;

  function clearLight() {
    pointer = null;
    globalThis.cancelAnimationFrame?.(frame);
    frame = 0;
    target.classList.remove("has-pointer-light");
  }

  function clear() {
    clearLight();
    clearRelations();
  }

  function pointerMove(event) {
    if (target.classList.contains?.("is-node-dragging")
      || target.classList.contains?.("is-panning")) {
      clearLight();
      return;
    }
    pointer = { x: event.clientX, y: event.clientY };
    if (frame) return;
    frame = requestAnimationFrame(() => {
      frame = 0;
      if (!pointer) return;
      const bounds = target.getBoundingClientRect();
      if (!bounds.width || !bounds.height) return;
      const x = Math.max(0, Math.min(
        100, ((pointer.x - bounds.left) / bounds.width) * 100,
      ));
      const y = Math.max(0, Math.min(
        100, ((pointer.y - bounds.top) / bounds.height) * 100,
      ));
      target.style.setProperty("--graph-cursor-x", `${x.toFixed(2)}%`);
      target.style.setProperty("--graph-cursor-y", `${y.toFixed(2)}%`);
      target.classList.add("has-pointer-light");
    });
  }

  if (!pointerLightAllowed()) {
    target.onpointermove = null;
    target.onpointerleave = clear;
  } else {
    target.onpointermove = pointerMove;
    target.onpointerleave = clear;
  }
  return () => {
    clearLight();
    if (target.onpointermove === pointerMove) target.onpointermove = null;
    if (target.onpointerleave === clear) target.onpointerleave = null;
  };
}

export function bindKnowledgeGraphMotion(target) {
  const relations = bindNodeRelations(target);
  const destroyPointerLight = bindPointerLight(target, relations.clearHover);
  return {
    destroy() {
      destroyPointerLight();
      relations.destroy();
      target.classList.remove("has-pointer-light", "has-active-node");
    },
  };
}
