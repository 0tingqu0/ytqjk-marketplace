import assert from "node:assert/strict";
import test from "node:test";

import { stepGraphPhysics } from "../views/knowledge-graph-drag.js";

test("drag physics moves neighbors while keeping pinned nodes finite", () => {
  const points = new Map([
    ["a", { id: "a", x: 180, y: 100, vx: 0, vy: 0, cluster: "c" }],
    ["b", { id: "b", x: 80, y: 100, vx: 0, vy: 0, cluster: "c" }],
    ["c", { id: "c", x: 80, y: 160, vx: 0, vy: 0, cluster: "c" }],
  ]);
  const edges = [{ source: "a", target: "b", length: 50 }];
  const movable = new Set(["a", "b"]);

  stepGraphPhysics(points, edges, movable, "a", 1);

  assert.equal(points.get("a").x, 180);
  assert.ok(points.get("b").x > 80);

  let heat = 1;
  for (let index = 0; index < 180; index += 1) {
    stepGraphPhysics(points, edges, movable, "", heat);
    heat *= 0.93;
  }
  const values = [...points.values()].flatMap((point) => [
    point.x,
    point.y,
    point.vx,
    point.vy,
  ]);
  assert.ok(values.every(Number.isFinite));
  assert.deepEqual(
    { x: points.get("c").x, y: points.get("c").y },
    { x: 80, y: 160 },
  );
});

test("drag physics ignores density-filtered nodes during repulsion", () => {
  const points = new Map([
    ["visible", {
      id: "visible", x: 100, y: 100, vx: 0, vy: 0, cluster: "a",
    }],
    ["hidden", {
      id: "hidden", x: 101, y: 100, vx: 0, vy: 0, cluster: "a",
    }],
  ]);

  stepGraphPhysics(
    points, [], new Set(["visible"]), "", 1, new Set(["visible"]),
  );

  assert.equal(points.get("visible").x, 100);
  assert.equal(points.get("visible").vx, 0);
});
