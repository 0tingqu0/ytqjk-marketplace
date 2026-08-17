from __future__ import annotations

import json
import shutil
import subprocess
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
UPDATE_SCRIPT = (
    ROOT / "plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard"
    / "update.js"
)


class DashboardUpdateFrontendTest(unittest.TestCase):
    def test_lost_post_response_recovers_from_installed_version(self) -> None:
        node = shutil.which("node")
        if node is None:
            self.skipTest("Node.js is unavailable")
        harness = r"""
const fs = require("fs");
const listeners = {};
const classes = { toggle() {}, remove() {} };
function element(id) {
  return {
    id, hidden: id === "update-panel", disabled: false, textContent: "",
    classList: classes, setAttribute() {}, contains() { return false; },
    focus() {}, addEventListener(name, fn) { listeners[`${id}:${name}`] = fn; },
  };
}
const elements = Object.fromEntries([
  "update-panel", "update-status", "install-update", "version-trigger",
].map((id) => [id, element(id)]));
global.document = {
  getElementById(id) { return elements[id]; },
  addEventListener() {},
};
global.confirm = () => true;
global.setInterval = () => 0;
global.setTimeout = (fn) => { fn(); return 0; };
let calls = 0;
global.fetch = async (_url, options = {}) => {
  calls += 1;
  if (options.method === "POST") throw new TypeError("Failed to fetch");
  const current = calls === 1 ? "0.4.9" : "0.4.10";
  return {
    ok: true,
    async json() {
      return {
        current_version: current,
        latest_version: "0.4.10",
        update_available: current === "0.4.9",
        token: "secret",
      };
    },
  };
};
eval(fs.readFileSync(process.argv[1], "utf8"));
setImmediate(async () => {
  await listeners["install-update:click"]();
  console.log(JSON.stringify({
    status: elements["update-status"].textContent,
    version: elements["version-trigger"].textContent,
    calls,
  }));
});
"""
        completed = subprocess.run(
            [node, "-e", harness, str(UPDATE_SCRIPT)],
            capture_output=True,
            text=True,
            encoding="utf-8",
            check=False,
            timeout=10,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        result = json.loads(completed.stdout.strip().splitlines()[-1])
        self.assertEqual(result["status"], "已更新至 v0.4.10，重启 Codex 生效")
        self.assertEqual(result["version"], "v0.4.10")
        self.assertGreaterEqual(result["calls"], 3)


if __name__ == "__main__":
    unittest.main()
