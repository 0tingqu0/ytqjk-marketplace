const MIN_SCALE = 1 / 3;
const MAX_SCALE = 1.5;
const ZOOM_IN = 0.8;
const ZOOM_OUT = 1.25;

function clamp(value, minimum, maximum) {
  return Math.max(minimum, Math.min(maximum, value));
}

function boundedViewBox(box, base, ratio = base.width / base.height) {
  const width = clamp(
    box.width, base.width * MIN_SCALE, base.width * MAX_SCALE,
  );
  const height = width / ratio;
  return {
    x: width >= base.width
      ? base.x - (width - base.width) / 2
      : clamp(box.x, base.x, base.x + base.width - width),
    y: height >= base.height
      ? base.y - (height - base.height) / 2
      : clamp(box.y, base.y, base.y + base.height - height),
    width,
    height,
  };
}

export function zoomViewBox(box, factor, anchor, base) {
  const ratio = box.width / box.height || base.width / base.height;
  const nextWidth = clamp(
    box.width * factor, base.width * MIN_SCALE, base.width * MAX_SCALE,
  );
  const nextHeight = nextWidth / ratio;
  const horizontal = (anchor.x - box.x) / box.width;
  const vertical = (anchor.y - box.y) / box.height;
  return boundedViewBox({
    x: anchor.x - horizontal * nextWidth,
    y: anchor.y - vertical * nextHeight,
    width: nextWidth,
    height: nextHeight,
  }, base, ratio);
}

function panViewBox(box, dx, dy, base) {
  return boundedViewBox({
    ...box,
    x: box.x + dx,
    y: box.y + dy,
  }, base, box.width / box.height);
}

function parseViewBox(svg) {
  const values = svg.getAttribute("viewBox").split(/\s+/).map(Number);
  return {
    x: values[0], y: values[1], width: values[2], height: values[3],
  };
}

function pointInViewBox(svg, box, event) {
  const bounds = svg.getBoundingClientRect();
  return {
    x: box.x + ((event.clientX - bounds.left) / bounds.width) * box.width,
    y: box.y + ((event.clientY - bounds.top) / bounds.height) * box.height,
  };
}

function nodePoint(svg, nodeId) {
  const node = [...svg.querySelectorAll("[data-node]")].find(
    (candidate) => candidate.dataset.node === nodeId,
  );
  const match = node?.getAttribute("transform")?.match(
    /translate\(([-\d.]+)[ ,]([-\d.]+)\)/,
  );
  return match ? { x: Number(match[1]), y: Number(match[2]) } : null;
}

export function bindGraphViewport(svg, target, onChange) {
  const base = parseViewBox(svg);
  let box = { ...base };
  let drag = null;
  let fitFrame = 0;
  let panFrame = 0;
  let lastChangeKey = "";
  let lastFitNodeIds = null;

  function render() {
    svg.setAttribute(
      "viewBox", `${box.x} ${box.y} ${box.width} ${box.height}`,
    );
    const percent = Math.round((base.width / box.width) * 100);
    target.dataset.graphZoom = String(percent);
    target.classList.toggle("graph-zoom-far", percent < 80);
    target.classList.toggle("graph-zoom-near", percent >= 130);
    target.classList.toggle("graph-zoom-detail", percent >= 190);
    const state = {
      percent,
      canZoomIn: box.width > base.width * MIN_SCALE + 0.1,
      canZoomOut: box.width < base.width * MAX_SCALE - 0.1,
    };
    const changeKey = `${state.percent}:${state.canZoomIn}:${state.canZoomOut}`;
    if (changeKey === lastChangeKey) return;
    lastChangeKey = changeKey;
    onChange?.(state);
  }

  function zoom(factor, anchor = {
    x: box.x + box.width / 2,
    y: box.y + box.height / 2,
  }) {
    lastFitNodeIds = null;
    box = zoomViewBox(box, factor, anchor, base);
    render();
  }

  function reset() {
    lastFitNodeIds = null;
    box = { ...base };
    render();
  }

  function focus(nodeId) {
    const point = nodePoint(svg, nodeId);
    if (!point) return;
    lastFitNodeIds = null;
    const width = Math.min(box.width, base.width / 1.65);
    const ratio = box.width / box.height;
    const height = width / ratio;
    box = boundedViewBox({
      x: point.x - width / 2,
      y: point.y - height / 2,
      width,
      height,
    }, base, ratio);
    render();
  }

  function fitNow() {
    const nodeIds = lastFitNodeIds;
    if (!nodeIds) return;
    const points = [...nodeIds].map((nodeId) => nodePoint(svg, nodeId))
      .filter(Boolean);
    if (!points.length) return;
    const xValues = points.map((point) => point.x);
    const yValues = points.map((point) => point.y);
    const minimumX = Math.min(...xValues);
    const maximumX = Math.max(...xValues);
    const minimumY = Math.min(...yValues);
    const maximumY = Math.max(...yValues);
    const bounds = svg.getBoundingClientRect();
    const compact = bounds.width < 680;
    const ratio = compact && bounds.height
      ? bounds.width / bounds.height
      : base.width / base.height;
    const horizontalPadding = compact ? 56 : 140;
    const verticalPadding = compact ? 44 : 110;
    const width = Math.max(
      base.width * MIN_SCALE,
      maximumX - minimumX + horizontalPadding,
      (maximumY - minimumY + verticalPadding) * ratio,
    );
    box = boundedViewBox({
      x: (minimumX + maximumX - width) / 2,
      y: (minimumY + maximumY - width / ratio) / 2,
      width,
      height: width / ratio,
    }, base, ratio);
    render();
  }

  function fit(nodeIds) {
    lastFitNodeIds = new Set(nodeIds);
    fitNow();
  }

  function scheduleFit() {
    if (!lastFitNodeIds) return;
    cancelAnimationFrame(fitFrame);
    fitFrame = requestAnimationFrame(fitNow);
  }

  function schedulePanRender() {
    if (panFrame) return;
    panFrame = requestAnimationFrame(() => {
      panFrame = 0;
      render();
    });
  }

  function wheel(event) {
    event.preventDefault();
    zoom(event.deltaY < 0 ? ZOOM_IN : ZOOM_OUT, pointInViewBox(
      svg, box, event,
    ));
  }

  function keydown(event) {
    if (event.defaultPrevented) return;
    if (event.key === "+" || event.key === "=") {
      event.preventDefault();
      zoom(ZOOM_IN);
      return;
    }
    if (event.key === "-" || event.key === "_") {
      event.preventDefault();
      zoom(ZOOM_OUT);
      return;
    }
    const step = event.shiftKey ? 0.14 : 0.05;
    const movement = {
      "ArrowLeft": [-box.width * step, 0],
      "ArrowRight": [box.width * step, 0],
      "ArrowUp": [0, -box.height * step],
      "ArrowDown": [0, box.height * step],
    }[event.key];
    if (!movement) return;
    event.preventDefault();
    box = panViewBox(box, movement[0], movement[1], base);
    render();
  }

  function pointerDown(event) {
    if (event.button !== 0 || event.target.closest(".graph-node-link")) return;
    lastFitNodeIds = null;
    svg.focus({ preventScroll: true });
    drag = {
      x: event.clientX,
      y: event.clientY,
      box: { ...box },
      bounds: svg.getBoundingClientRect(),
      pointerId: event.pointerId,
    };
    svg.setPointerCapture(event.pointerId);
    target.classList.add("is-panning");
  }

  function pointerMove(event) {
    if (!drag || drag.pointerId !== event.pointerId) return;
    const dx = -((event.clientX - drag.x) / drag.bounds.width) * drag.box.width;
    const dy = -((event.clientY - drag.y) / drag.bounds.height) * drag.box.height;
    box = panViewBox(drag.box, dx, dy, base);
    schedulePanRender();
  }

  function pointerUp(event) {
    if (!drag || drag.pointerId !== event.pointerId) return;
    drag = null;
    target.classList.remove("is-panning");
    if (svg.hasPointerCapture(event.pointerId)) {
      svg.releasePointerCapture(event.pointerId);
    }
  }

  svg.addEventListener("wheel", wheel, { passive: false });
  svg.addEventListener("keydown", keydown);
  svg.addEventListener("pointerdown", pointerDown);
  svg.addEventListener("pointermove", pointerMove);
  svg.addEventListener("pointerup", pointerUp);
  svg.addEventListener("pointercancel", pointerUp);
  svg.addEventListener("dblclick", (event) => {
    if (!event.target.closest(".graph-node-link")) reset();
  });
  const resizeObserver = new ResizeObserver(scheduleFit);
  resizeObserver.observe(svg);
  render();
  return {
    zoomIn: () => zoom(ZOOM_IN),
    zoomOut: () => zoom(ZOOM_OUT),
    reset,
    focus,
    fit,
    clearFit: () => { lastFitNodeIds = null; },
    destroy() {
      cancelAnimationFrame(fitFrame);
      cancelAnimationFrame(panFrame);
      if (drag && svg.hasPointerCapture?.(drag.pointerId)) {
        svg.releasePointerCapture(drag.pointerId);
      }
      drag = null;
      target.classList.remove("is-panning");
      resizeObserver.disconnect();
    },
  };
}
