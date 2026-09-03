import assert from "node:assert/strict";
import test from "node:test";

import { visibleGraphClusters } from "../views/knowledge-graph-clusters.js";

function graphNode({ cluster, x, y, visible = true }) {
  return {
    dataset: {
      cluster,
      x: String(x),
      y: String(y),
    },
    classList: {
      contains(className) {
        return className === "is-filtered-out" && !visible;
      },
    },
  };
}

test("cluster halos are derived only from nodes left visible by density filtering", () => {
  const clusterLabels = new Map([
    ["alpha", "核心域"],
    ["beta", "外围域"],
    ["solo", "单节点域"],
    ["hidden", "已隐藏域"],
  ]);
  const visibleNodes = [
    graphNode({ cluster: "alpha", x: 100, y: 100 }),
    graphNode({ cluster: "alpha", x: 300, y: 100 }),
    graphNode({ cluster: "beta", x: 500, y: 200 }),
    graphNode({ cluster: "beta", x: 540, y: 200 }),
    graphNode({ cluster: "solo", x: 700, y: 300 }),
  ];
  const densityFilteredNodes = [
    ...visibleNodes,
    graphNode({ cluster: "alpha", x: 1_900, y: 1_500, visible: false }),
    graphNode({ cluster: "solo", x: 900, y: 700, visible: false }),
    graphNode({ cluster: "hidden", x: 1_000, y: 800, visible: false }),
    graphNode({ cluster: "hidden", x: 1_200, y: 800, visible: false }),
  ];

  const visibleOnlyLayout = visibleGraphClusters(visibleNodes, clusterLabels);
  const filteredLayout = visibleGraphClusters(
    densityFilteredNodes,
    clusterLabels,
  );

  assert.deepEqual(
    filteredLayout,
    visibleOnlyLayout,
    "filtered-out nodes must not move or resize a visible cluster halo",
  );
  assert.deepEqual(filteredLayout, [
    {
      id: "alpha",
      label: "核心域",
      count: 2,
      x: 200,
      y: 100,
      radius: 132,
    },
    {
      id: "beta",
      label: "外围域",
      count: 2,
      x: 520,
      y: 200,
      radius: 72,
    },
  ]);
});
