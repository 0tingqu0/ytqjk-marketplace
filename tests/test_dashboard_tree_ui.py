from __future__ import annotations

import json
import shutil
import subprocess
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
TREE_DIALOG = (
    ROOT
    / "plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard/js/ui"
    / "tree-dialog.js"
)
INDEX = TREE_DIALOG.parents[2] / "index.html"


def test_tree_identifier_input_remains_editable() -> None:
    html = INDEX.read_text(encoding="utf-8")
    start = html.index('<input id="tree-node-id"')
    field = html[start:html.index(">", start)]

    assert "readonly" not in field
    assert "disabled" not in field


def test_create_dialog_generates_editable_stable_identifier() -> None:
    node = shutil.which("node")
    if node is None:
        raise unittest.SkipTest("Node.js is unavailable")
    source = r"""
import assert from "node:assert/strict";

class Element {
  constructor(tag = "div") {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.disabled = false;
    this.hidden = false;
    this.open = false;
    this.selected = false;
    this.textContent = "";
    this.value = "";
    this.control = null;
  }
  close() { this.open = false; }
  querySelector() { return this.control; }
  replaceChildren(...children) { this.children = children; }
  showModal() { this.open = true; }
}

const elements = new Map();
function element(id) {
  if (!elements.has(id)) elements.set(id, new Element());
  return elements.get(id);
}

globalThis.document = {
  createElement: (tag) => new Element(tag),
  getElementById: element,
};

const fields = {
  "tree-node-id-field": "tree-node-id",
  "tree-title-field": "tree-title",
  "tree-node-type-field": "tree-node-type",
  "tree-parent-id-field": "tree-parent-id",
  "tree-middle-id-field": "tree-middle-id",
  "tree-mount-id-field": "tree-mount-id",
  "tree-capability-field": "tree-capability",
};
for (const [wrapperId, controlId] of Object.entries(fields)) {
  element(wrapperId).control = element(controlId);
}

const form = element("tree-action-form");
form.reset = () => {
  for (const controlId of Object.values(fields)) {
    element(controlId).value = "";
  }
  element("tree-node-type").value = "group";
};

let seed = 0;
Object.defineProperty(globalThis, "crypto", {
  configurable: true,
  value: {
    getRandomValues: (bytes) => {
      bytes.forEach((_, index) => { bytes[index] = seed + index; });
      seed += bytes.length;
      return bytes;
    },
  },
});

const dialog = await import(__TREE_DIALOG__);
dialog.bindTreeDialog(() => {});
dialog.openTreeAction("create", null, { nodes: [] });

const nodeId = element("tree-node-id");
assert.equal(nodeId.value, "library-000102030405060708090a0b0c0d0e0f");
assert.match(nodeId.value, /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/);
assert.equal(nodeId.disabled, false);

nodeId.value = "custom.library";
element("tree-title").value = "新的知识库名称";
form.oninput({ target: element("tree-title") });
assert.equal(nodeId.value, "custom.library");

element("tree-dialog").close();
dialog.openTreeAction("create", null, { nodes: [] });
assert.equal(nodeId.value, "library-101112131415161718191a1b1c1d1e1f");
""".replace("__TREE_DIALOG__", json.dumps(TREE_DIALOG.as_uri()))

    result = subprocess.run(
        [node, "--input-type=module", "-e", source],
        capture_output=True,
        encoding="utf-8",
        timeout=10,
        check=False,
    )

    assert result.returncode == 0, result.stdout + result.stderr
