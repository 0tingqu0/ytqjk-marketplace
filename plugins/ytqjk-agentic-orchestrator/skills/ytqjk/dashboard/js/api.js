function errorFrom(body, fallback) {
  if (body && typeof body.error === "string") return body.error;
  const detail = body && body.error;
  if (detail && typeof detail.message === "string") return detail.message;
  if (detail && typeof detail.code === "string") return detail.code;
  return fallback;
}

async function request(path, options = {}) {
  let response;
  try {
    response = await fetch(path, { cache: "no-store", ...options });
  } catch {
    throw new Error("无法连接本地知识工作台");
  }
  let body = {};
  try {
    body = await response.json();
  } catch { /* Server did not return JSON. */ }
  if (!response.ok) throw new Error(errorFrom(body, "请求失败"));
  return body;
}

function post(path, payload) {
  return request(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
}

function uploadIntake(payload, onProgress) {
  const body = JSON.stringify(payload);
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", "/api/intake");
    xhr.setRequestHeader("Content-Type", "application/json");
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) onProgress(event.loaded, event.total);
    };
    xhr.onerror = () => reject(new Error("无法连接本地知识工作台"));
    xhr.onload = () => {
      let result = {};
      try {
        result = JSON.parse(xhr.responseText);
      } catch { /* Handled below. */ }
      if (xhr.status < 200 || xhr.status >= 300) {
        reject(new Error(errorFrom(result, "投递失败")));
        return;
      }
      resolve(result);
    };
    xhr.send(body);
  });
}

function intakeJobPath(id) {
  return `/api/intake/jobs/${encodeURIComponent(id)}`;
}

const TERMINAL_JOBS = new Set(["SUCCEEDED", "FAILED", "CANCELLED"]);
const JOB_STATES = new Set([
  "QUEUED", "RUNNING", "SUCCEEDED", "FAILED", "CANCELLED",
]);
const JOB_STAGES = new Set([
  "validate", "security-scan", "page-detect", "native-extract", "render",
  "ocr-primary", "ocr-review", "layout-table", "normalize", "chunk",
  "candidate-write", "complete",
]);
const JOB_ID = /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/;
const intakePolls = new Map();
const latestJobs = new Map();

export function isIntakeTerminal(state) {
  return TERMINAL_JOBS.has(state);
}

export function intakeJobFrom(body) {
  const job = body?.job;
  const pageCount = job?.page_count;
  const valid = job && JOB_ID.test(job.id)
    && JOB_STATES.has(job.state) && JOB_STAGES.has(job.stage)
    && Number.isInteger(job.progress) && job.progress >= 0
    && job.progress <= 100 && Number.isSafeInteger(job.revision)
    && job.revision >= 0 && (pageCount === null
      || (Number.isInteger(pageCount) && pageCount >= 1
        && pageCount <= 10000));
  if (!valid) throw new Error("入库任务响应格式无效");
  return job;
}

function acceptJob(job) {
  const current = latestJobs.get(job.id);
  const revision = job.revision;
  const currentRevision = current?.revision ?? -1;
  if (current && revision < currentRevision) return current;
  if (current && revision === currentRevision
      && isIntakeTerminal(current.state)) return current;
  latestJobs.set(job.id, job);
  return job;
}

function pause() {
  return new Promise((resolve) => setTimeout(resolve, 750));
}

async function pollIntake(initial, onUpdate) {
  let job = acceptJob(initial);
  onUpdate(job);
  while (!isIntakeTerminal(job.state)) {
    const known = latestJobs.get(job.id);
    if (known && isIntakeTerminal(known.state)) {
      job = known;
    } else {
      job = acceptJob(intakeJobFrom(await request(intakeJobPath(job.id))));
    }
    onUpdate(job);
    if (!isIntakeTerminal(job.state)) await pause();
  }
  return job;
}

export function watchIntakeJob(initial, onUpdate) {
  if (intakePolls.has(initial.id)) return intakePolls.get(initial.id);
  const task = pollIntake(initial, onUpdate).finally(() => {
    intakePolls.delete(initial.id);
  });
  intakePolls.set(initial.id, task);
  return task;
}

async function intakeAction(id, action) {
  const body = await request(`${intakeJobPath(id)}/${action}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: "{}",
  });
  acceptJob(intakeJobFrom(body));
  return body;
}

export const api = {
  snapshot: () => request("/api/snapshot"),
  tree: () => request("/api/libraries/tree"),
  treePreview: (action, payload) => request("/api/libraries/preview", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ action, payload }),
  }),
  treeCommit: (action, payload) => request(
    `/api/libraries/${action.replace("_", "-")}`,
    {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
    },
  ),
  globalLibrary: () => request("/api/global-library"),
  projectLibrary: (id) => request(
    `/api/project-library?id=${encodeURIComponent(id)}`,
  ),
  peers: () => request("/api/peers"),
  peerBootstrap: () => post("/api/peers/bootstrap", {}),
  peerConfigure: (payload) => post("/api/peers/configure", payload),
  peerSecret: () => post("/api/peers/secret", {}),
  peerUpsert: (payload) => post("/api/peers/upsert", payload),
  peerRemove: (payload) => post("/api/peers/remove", payload),
  peerHealth: (payload) => post("/api/peers/health", payload),
  peerDispatch: (payload) => post("/api/peers/dispatch", payload),
  peerMaterial: (payload) => post("/api/peers/material", payload),
  document: (path) => request(`/api/document?path=${encodeURIComponent(path)}`),
  intake: uploadIntake,
  intakeJob: (id) => request(intakeJobPath(id)),
  retryIntake: (id) => intakeAction(id, "retry"),
  cancelIntake: (id) => intakeAction(id, "cancel"),
  candidate: (method, payload, endpoint = "/api/candidate") => request(
    endpoint,
    {
      method,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload),
    },
  ),
};
