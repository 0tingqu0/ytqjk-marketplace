from __future__ import annotations

import json
import subprocess
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
DASHBOARD = SCRIPTS.parent / "dashboard"
API = DASHBOARD / "js" / "api.js"
INTAKE = DASHBOARD / "js" / "views" / "intake.js"


def _node(source: str) -> None:
    result = subprocess.run(
        ["node", "--input-type=module", "-e", source],
        capture_output=True,
        encoding="utf-8",
        timeout=10,
        check=False,
    )
    assert result.returncode == 0, result.stdout + result.stderr


def test_intake_javascript_stays_small_and_wires_job_contract() -> None:
    for path in (API, INTAKE):
        lines = path.read_text(encoding="utf-8").splitlines()
        effective = [
            line for line in lines
            if line.strip() and not line.lstrip().startswith("//")
        ]
        assert len(effective) <= 300
        assert max(map(len, lines)) <= 80
    api_source = API.read_text(encoding="utf-8")
    intake_source = INTAKE.read_text(encoding="utf-8")
    assert "/api/intake/jobs/" in api_source
    assert "retryIntake" in api_source
    assert "cancelIntake" in api_source
    assert "localStorage" not in intake_source
    assert "saveIntakeResults" in intake_source
    assert "candidate?.chunks" in intake_source


DOM_TEST = r"""
import assert from "node:assert/strict";

class Element {
  constructor(tag = "div") {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.style = {};
    this.hidden = false;
    this.textContent = "";
    this.value = "";
    this.className = "";
    this.disabled = false;
    const names = new Set();
    this.classList = {
      add: (name) => names.add(name),
      remove: (name) => names.delete(name),
    };
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  querySelector(selector) {
    return selector === ".drop-zone" ? dropZone : null;
  }
}

const elements = new Map();
const dropZone = new Element("label");
globalThis.document = {
  createElement: (tag) => new Element(tag),
  getElementById: (id) => {
    if (!elements.has(id)) elements.set(id, new Element());
    return elements.get(id);
  },
};
const saved = new Map();
globalThis.localStorage = {
  getItem: (key) => saved.get(key) ?? null,
  setItem: (key, value) => saved.set(key, value),
};

const ids = {
  restored: "11111111-1111-1111-1111-111111111111",
  invalid: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
  retry: "22222222-2222-2222-2222-222222222222",
  cancel: "33333333-3333-3333-3333-333333333333",
  folder: "44444444-4444-4444-4444-444444444444",
  error: "55555555-5555-5555-5555-555555555555",
  refresh: "66666666-6666-6666-6666-666666666666",
};
const calls = [];
function job(id, state, revision) {
  const value = {
    id, state, revision, stage: "complete",
    progress: state === "SUCCEEDED" ? 100 : 40,
    page_count: 3,
  };
  if (state === "SUCCEEDED") {
    value.result = {
      candidate: {
        state: "CANDIDATE",
        chunks: [{}, {}, {}, {}],
      },
    };
  }
  return value;
}
globalThis.fetch = async (path, options = {}) => {
  calls.push([path, options.method || "GET", options]);
  if (path.includes(ids.error)) {
    return {
      ok: false,
      json: async () => ({ error: "EXPECTED_POLL_FAILURE" }),
    };
  }
  let value;
  if (path.endsWith("/retry")) value = job(ids.retry, "QUEUED", 2);
  else if (path.endsWith("/cancel")) {
    value = job(ids.cancel, "CANCELLED", 2);
  } else if (path.includes(ids.cancel)) {
    value = job(ids.cancel, "RUNNING", 1);
  } else if (path.includes(ids.retry)) {
    value = job(ids.retry, "SUCCEEDED", 3);
  } else if (path.includes(ids.refresh)) {
    value = job(ids.refresh, "SUCCEEDED", 2);
  } else if (path.includes(ids.folder)) {
    value = job(ids.folder, "SUCCEEDED", 2);
  } else {
    value = job(ids.restored, "SUCCEEDED", 2);
  }
  return { ok: true, json: async () => ({ job: value }) };
};

const { api } = await import(__API__);
const intake = await import(__INTAKE__);
let refreshes = 0;
const refresh = async () => { refreshes += 1; };
const restored = {
  intakeResults: [
    {
      name: "scan.pdf", message: "QUEUED", jobId: ids.restored,
      jobState: "QUEUED", jobStage: "validate", jobProgress: 0,
      pageCount: null, jobRevision: 1,
    },
    {
      name: "bad.pdf", message: "RUNNING", jobId: ids.invalid,
      jobState: "RUNNING", jobStage: "ocr-primary", jobProgress: 20,
      pageCount: 1, jobRevision: "Infinity",
    },
  ],
};
intake.bindIntake(restored, refresh);
intake.bindIntake(restored, refresh);
await new Promise((resolve) => setTimeout(resolve, 20));
assert.equal(
  calls.filter(([path]) => path.includes(ids.restored)).length,
  1,
);
assert.equal(restored.intakeResults[0].jobState, "SUCCEEDED");
assert.equal(restored.intakeResults.length, 1);
assert.equal(calls.some(([path]) => path.includes(ids.invalid)), false);
assert.match(restored.intakeResults[0].message, /CANDIDATE.*4/);
assert.match(elements.get("intake-stage").textContent, /complete.*3 页/);
assert.equal(elements.get("intake-percent").textContent, "100%");
assert.match([...saved.values()].join(""), new RegExp(ids.restored));
assert.ok(refreshes >= 1);

const refreshFailure = {
  intakeResults: [{
    name: "saved.xlsx", message: "RUNNING", jobId: ids.refresh,
    jobState: "RUNNING", jobStage: "candidate-write", jobProgress: 90,
    pageCount: 3, jobRevision: 1,
  }],
};
intake.bindIntake(refreshFailure, async () => {
  throw new Error("EXPECTED_REFRESH_FAILURE");
});
await new Promise((resolve) => setTimeout(resolve, 20));
assert.equal(refreshFailure.intakeResults[0].jobState, "SUCCEEDED");
assert.match(refreshFailure.intakeResults[0].message, /CANDIDATE.*4/);
assert.match(elements.get("intake-status").textContent, /列表刷新失败/);
assert.doesNotMatch(elements.get("intake-stage").textContent, /处理失败/);

function find(node, label) {
  if (node.textContent === label) return node;
  for (const child of node.children) {
    const found = find(child, label);
    if (found) return found;
  }
  return null;
}
const failed = {
  intakeResults: [{
    name: "retry.png", message: "NOT_CONFIGURED",
    jobId: ids.retry, jobState: "FAILED", jobStage: "ocr",
    jobProgress: 40, pageCount: 3, jobRevision: 1,
    retryable: true,
  }],
};
intake.bindIntake(failed, refresh);
const retry = find(elements.get("intake-results"), "重试");
assert.ok(retry);
await retry.onclick();
assert.equal(failed.intakeResults[0].jobState, "SUCCEEDED");

const active = {
  intakeResults: [{
    name: "cancel.pdf", message: "RUNNING",
    jobId: ids.cancel, jobState: "RUNNING", jobStage: "layout-table",
    jobProgress: 40, pageCount: 3, jobRevision: 1,
  }],
};
intake.bindIntake(active, refresh);
const cancel = find(elements.get("intake-results"), "取消");
assert.ok(cancel);
await cancel.onclick();
assert.equal(active.intakeResults[0].jobState, "CANCELLED");
assert.ok(calls.some(([path, method]) => (
  path.endsWith("/retry") && method === "POST"
)));
assert.ok(calls.some(([path, method]) => (
  path.endsWith("/cancel") && method === "POST"
)));
for (const [path, method, options] of calls) {
  if (method === "POST" && /\/(retry|cancel)$/.test(path)) {
    assert.equal(options.headers["Content-Type"], "application/json");
    assert.equal(options.body, "{}");
  }
}

globalThis.FileReader = class {
  readAsDataURL() {
    this.result = "data:application/octet-stream;base64,QQ==";
    this.onload();
  }
};
api.intake = async (payload) => {
  if (payload.name === "image.png") {
    return { job: job(ids.folder, "QUEUED", 1) };
  }
  if (payload.name === "waiting.txt") {
    return {
      chunks: 2,
      assessment: { decision: "NEEDS_MORE_EVIDENCE" },
    };
  }
  throw new Error("EXPECTED_FOLDER_FAILURE");
};
const folderState = { intakeResults: [] };
intake.bindIntake(folderState, refresh);
const files = [
  { name: "image.png", size: 1, webkitRelativePath: "set/image.png" },
  { name: "waiting.txt", size: 1, webkitRelativePath: "set/waiting.txt" },
  { name: "bad.txt", size: 1, webkitRelativePath: "set/bad.txt" },
];
const target = { files, value: "selected" };
await elements.get("folder-input").onchange({ target });
assert.match(
  elements.get("intake-status").textContent,
  /可复审 1 · 待补充 1 · 失败 1/,
);

const errored = {
  intakeResults: [{
    name: "network.pdf", message: "RUNNING",
    jobId: ids.error, jobState: "RUNNING", jobStage: "ocr-primary",
    jobProgress: 55, pageCount: 2, jobRevision: 1,
  }],
};
intake.bindIntake(errored, refresh);
await new Promise((resolve) => setTimeout(resolve, 20));
assert.equal(errored.intakeResults[0].jobState, "RUNNING");
assert.match(errored.intakeResults[0].message, /EXPECTED_POLL_FAILURE/);
assert.doesNotMatch(errored.intakeResults[0].message, /已保存/);
"""


def test_dom_restores_polling_and_exposes_retry_cancel() -> None:
    source = DOM_TEST.replace("__API__", json.dumps(API.as_uri()))
    source = source.replace("__INTAKE__", json.dumps(INTAKE.as_uri()))
    _node(source)


CONTRACT_TEST = r"""
import assert from "node:assert/strict";
const intake = await import(__INTAKE__);
const failed = {
  state: "FAILED",
  result: { status: "NOT_CONFIGURED", retryable: true },
  error: { category: "TRANSIENT", ref: "MODEL_MISSING" },
};
assert.match(intake.jobMessage(failed), /NOT_CONFIGURED/);
assert.match(intake.jobMessage(failed), /可重试/);
assert.throws(
  () => intake.jobMessage({ state: "SUCCEEDED", result: {} }),
  /candidate\/chunks/,
);
assert.throws(
  () => intake.jobMessage({
    state: "SUCCEEDED",
    result: { candidate: { state: "APPROVED", chunks: [] } },
  }),
  /candidate\/chunks/,
);
"""


def test_failed_and_malformed_success_messages_are_truthful() -> None:
    source = CONTRACT_TEST.replace(
        "__INTAKE__", json.dumps(INTAKE.as_uri())
    )
    _node(source)
