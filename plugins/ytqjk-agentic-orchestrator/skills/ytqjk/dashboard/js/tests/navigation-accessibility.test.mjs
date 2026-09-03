import assert from "node:assert/strict";
import test from "node:test";

import { resolveTheme } from "../dashboard-controls.js";

test("system theme resolves from the operating system preference", () => {
  assert.equal(resolveTheme("system", true), "dark");
  assert.equal(resolveTheme("system", false), "light");
  assert.equal(resolveTheme("dark", false), "dark");
  assert.equal(resolveTheme("light", true), "light");
  assert.equal(resolveTheme("unsupported", true), "dark");
});

test("router reports only real route transitions as changed", async () => {
  const previousWindow = globalThis.window;
  const previousHistory = globalThis.history;
  const listeners = new Map();
  const routeCalls = [];
  let scrollCalls = 0;
  globalThis.window = {
    location: { hash: "#overview" },
    addEventListener(type, listener) {
      listeners.set(type, listener);
    },
    matchMedia() {
      return { matches: false };
    },
    scrollTo() { scrollCalls += 1; },
  };
  globalThis.history = {
    replaceState(_state, _title, hash) {
      globalThis.window.location.hash = hash;
    },
  };

  try {
    const moduleUrl = new URL(`../router.js?test=${Date.now()}`, import.meta.url);
    const { createRouter } = await import(moduleUrl);
    const router = createRouter((route, changed) => {
      routeCalls.push([route, changed]);
    });

    router.go("overview");
    globalThis.window.location.hash = "#intake";
    listeners.get("hashchange")();

    assert.deepEqual(routeCalls, [
      ["overview", false],
      ["overview", false],
      ["intake", true],
    ]);
    assert.equal(scrollCalls, 1);
  } finally {
    globalThis.window = previousWindow;
    globalThis.history = previousHistory;
  }
});
