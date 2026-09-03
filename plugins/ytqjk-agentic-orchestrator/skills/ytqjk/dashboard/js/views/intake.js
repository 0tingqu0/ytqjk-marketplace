import {
  api, intakeJobFrom, isIntakeTerminal, watchIntakeJob,
} from "../api.js";
import { byId, button, clear, text } from "../ui/dom.js";
import { saveIntakeResults } from "../store.js";
import { bindIntakeForm } from "./intake-form.js";
import { refreshAfterSuccess } from "./intake_refresh.js";

function progress(stage, loaded, total) {
  const box = byId("intake-progress");
  const bar = byId("intake-progress-bar");
  box.hidden = false;
  byId("intake-stage").textContent = stage;
  if (!total) {
    byId("intake-percent").textContent = "处理中";
    bar.classList.add("is-indeterminate");
    bar.style.width = "";
    return;
  }
  const percent = Math.min(100, Math.round((loaded / total) * 100));
  byId("intake-percent").textContent = `${percent}%`;
  bar.classList.remove("is-indeterminate");
  bar.style.width = `${percent}%`;
}

function setStatus(message) {
  byId("intake-status").textContent = message;
}

function retryable(job) {
  return job.result?.retryable === true || job.error?.retryable === true;
}

function pageLabel(job) {
  return Number.isInteger(job.page_count)
    ? `${job.page_count} 页`
    : "页数检测中";
}

export function jobMessage(job) {
  if (job.state === "SUCCEEDED") {
    const candidate = job.result?.candidate;
    const chunks = candidate?.chunks;
    const chunksValid = Array.isArray(chunks) && chunks.every(
      (item) => item && typeof item === "object" && !Array.isArray(item),
    );
    if (!candidate || candidate.state !== "CANDIDATE" || !chunksValid) {
      throw new Error("SUCCEEDED 响应缺少真实 candidate/chunks");
    }
    return `已保存为 CANDIDATE，拆分 ${chunks.length} 个知识片段`;
  }
  if (job.state === "FAILED") {
    const details = [
      job.result?.status, job.error?.category, job.error?.ref,
    ].filter((value, index, rows) => (
      typeof value === "string" && rows.indexOf(value) === index
    ));
    const reason = details.length ? details.join(" · ") : "FAILED";
    return `FAILED · ${reason} · ${retryable(job) ? "可重试" : "不可重试"}`;
  }
  if (job.state === "CANCELLED") return "任务已取消";
  return `${job.state} · ${job.stage} · ${job.progress}% · ${pageLabel(job)}`;
}

function tagged(error, jobId = "") {
  const result = error instanceof Error ? error : new Error("投递失败");
  if (jobId) result.jobId = jobId;
  result.preserveProgress = Boolean(jobId);
  return result;
}

function render(context) {
  const target = byId("intake-results");
  clear(target, context.state.intakeResults.map((row) => {
    const item = text("article", "", "compact-row");
    item.append(text("b", row.name), text("span", row.message));
    let action = "";
    if (row.jobState === "FAILED" && row.retryable) action = "retry";
    else if (row.jobId && !isIntakeTerminal(row.jobState)) action = "cancel";
    if (!action) return item;
    const controls = text("div", "", "form-actions");
    const control = button(action === "retry" ? "重试" : "取消");
    control.className = action === "cancel" ? "danger" : "secondary";
    control.onclick = async () => {
      control.disabled = true;
      try { await act(context, row, action); }
      catch (error) { context.reportError(error); }
      finally { control.disabled = false; }
    };
    controls.append(control);
    item.append(controls);
    return item;
  }));
}

function persist(context) {
  context.state.intakeResults.splice(12);
  saveIntakeResults(context.state.intakeResults);
  render(context);
}

function saveResult(context, row) {
  context.state.intakeResults.unshift(row);
  persist(context);
}

function applyJob(context, name, job) {
  let message;
  try { message = jobMessage(job); }
  catch (error) { message = error.message; }
  const rows = context.state.intakeResults;
  const index = rows.findIndex((row) => row.jobId === job.id);
  const row = {
    name, message, jobId: job.id, jobState: job.state,
    jobStage: job.stage, jobProgress: job.progress,
    pageCount: job.page_count, jobRevision: job.revision,
    retryable: retryable(job),
  };
  if (index < 0) rows.unshift(row);
  else rows.splice(index, 1, row);
  progress(`${job.stage} · ${pageLabel(job)}`, job.progress, 100);
  setStatus(message);
  persist(context);
}

function markPollError(context, jobId, message) {
  const row = context.state.intakeResults.find(
    (item) => item.jobId === jobId,
  );
  if (row && !isIntakeTerminal(row.jobState)) {
    row.message = `状态读取失败：${message}`;
    persist(context);
  }
}

async function follow(context, name, initial) {
  let current = initial;
  try {
    current = await watchIntakeJob(initial, (job) => {
      current = job;
      applyJob(context, name, job);
    });
  } catch (error) {
    markPollError(context, initial.id, error.message);
    throw tagged(error, initial.id);
  }
  if (current.state === "SUCCEEDED") {
    let message;
    try { message = jobMessage(current); }
    catch (error) { throw tagged(error, current.id); }
    await refreshAfterSuccess(context.refresh, setStatus, message);
    return current;
  }
  throw tagged(new Error(jobMessage(current)), current.id);
}

async function act(context, row, action) {
  const response = action === "retry"
    ? await api.retryIntake(row.jobId)
    : await api.cancelIntake(row.jobId);
  const job = intakeJobFrom(response);
  applyJob(context, row.name, job);
  if (!isIntakeTerminal(job.state)) await follow(context, row.name, job);
}

function readFile(file, onProgress) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onprogress = (event) => {
      if (event.lengthComputable) onProgress(event.loaded, event.total);
    };
    reader.onerror = () => reject(new Error("无法读取文件"));
    reader.onload = () => resolve(String(reader.result).split(",")[1]);
    reader.readAsDataURL(file);
  });
}

async function publish(
  context, name, content, encoding, purpose, relativePath = "",
) {
  progress("正在上传（等待字节事件）");
  const response = await api.intake(
    { name, content, encoding, purpose, relativePath },
    (loaded, total) => {
      const stage = loaded >= total
        ? "服务器已受理，等待任务状态"
        : "正在上传";
      progress(stage, loaded, total);
    },
  );
  if (response?.job) {
    return follow(context, name, intakeJobFrom(response));
  }
  const assessment = response.assessment || {};
  const ready = assessment.decision === "READY_FOR_REVIEW";
  const review = ready ? "可人工复审" : "等待补充资料";
  const message = (
    `已保存为 CANDIDATE，拆分 ${response.chunks ?? 0} 个知识片段：${review}`
  );
  progress("已完成", 1, 1);
  setStatus(message);
  saveResult(context, { name, message });
  await refreshAfterSuccess(context.refresh, setStatus, message);
  return response;
}

async function publishFile(context, file, purpose, relativePath = "") {
  progress(`正在读取 ${file.name}`, 0, file.size || 1);
  const content = await readFile(file, (loaded, total) => {
    progress(`正在读取 ${file.name}`, loaded, total);
  });
  return publish(
    context, file.name, content, "base64", purpose, relativePath,
  );
}

async function publishFolder(context, files, purpose) {
  let ready = 0;
  let waiting = 0;
  let failed = 0;
  const folder = { ...context, refresh: async () => {} };
  for (const [index, file] of files.entries()) {
    setStatus(`处理 ${index + 1}/${files.length}：${file.name}`);
    try {
      const result = await publishFile(
        folder, file, purpose, file.webkitRelativePath,
      );
      if (result.state === "SUCCEEDED"
          || result.assessment?.decision === "READY_FOR_REVIEW") ready += 1;
      else waiting += 1;
    } catch (error) {
      failed += 1;
      if (!error.jobId) {
        saveResult(context, { name: file.name, message: error.message });
      }
    }
    progress(
      `已完成 ${index + 1}/${files.length}`,
      index + 1,
      files.length,
    );
  }
  const message =
    `文件夹处理完成：可复审 ${ready} · 待补充 ${waiting} · 失败 ${failed}`;
  setStatus(message);
  persist(context);
  await refreshAfterSuccess(context.refresh, setStatus, message);
}

function restoredJob(row) {
  return intakeJobFrom({ job: {
    id: row.jobId, state: row.jobState,
    stage: row.jobStage,
    progress: row.jobProgress,
    page_count: row.pageCount ?? null,
    revision: row.jobRevision,
  } });
}

export function bindIntake(state, refresh) {
  const reportError = (error) => {
    if (!error?.preserveProgress) progress("处理失败", 1, 1);
    setStatus(error instanceof Error ? error.message : "投递失败");
  };
  const context = { state, refresh, reportError };
  render(context);
  for (const row of [...state.intakeResults]) {
    if (row.jobId && !isIntakeTerminal(row.jobState)) {
      try {
        follow(context, row.name, restoredJob(row)).catch(reportError);
      } catch {
        state.intakeResults.splice(state.intakeResults.indexOf(row), 1);
        persist(context);
      }
    }
  }
  bindIntakeForm(context, { publish, publishFile, publishFolder });
}
