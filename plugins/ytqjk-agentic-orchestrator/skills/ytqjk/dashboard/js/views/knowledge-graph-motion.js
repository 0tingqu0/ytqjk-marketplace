function clearRelations(target) {
  target.classList.remove("has-active-node");
  target.querySelectorAll(".is-active, .is-related").forEach((node) => {
    node.classList.remove("is-active", "is-related");
  });
}

function activateRelations(target, nodeId) {
  const related = new Set([nodeId]);
  target.querySelectorAll(".graph-edge").forEach((edge) => {
    const connected = edge.dataset.source === nodeId
      || edge.dataset.target === nodeId;
    edge.classList.toggle("is-related", connected);
    if (!connected) return;
    related.add(edge.dataset.source);
    related.add(edge.dataset.target);
  });
  target.querySelectorAll(".graph-node-link").forEach((link) => {
    link.classList.toggle("is-active", link.dataset.node === nodeId);
    link.classList.toggle("is-related", related.has(link.dataset.node));
  });
  target.classList.add("has-active-node");
}

function bindNodeRelations(target) {
  target.querySelectorAll(".graph-node-link").forEach((link, index) => {
    link.style.setProperty("--graph-order", String(index));
    const activate = () => activateRelations(target, link.dataset.node);
    link.onpointerenter = activate;
    link.onpointerleave = () => clearRelations(target);
    link.onfocus = activate;
    link.onblur = () => clearRelations(target);
  });
}

function bindPointerLight(target) {
  target.onpointermove = (event) => {
    const bounds = target.getBoundingClientRect();
    const x = ((event.clientX - bounds.left) / bounds.width) * 100;
    const y = ((event.clientY - bounds.top) / bounds.height) * 100;
    target.style.setProperty("--graph-cursor-x", `${x.toFixed(2)}%`);
    target.style.setProperty("--graph-cursor-y", `${y.toFixed(2)}%`);
    target.classList.add("has-pointer-light");
  };
  target.onpointerleave = () => {
    target.classList.remove("has-pointer-light");
    clearRelations(target);
  };
}

export function bindKnowledgeGraphMotion(target) {
  bindNodeRelations(target);
  bindPointerLight(target);
}
