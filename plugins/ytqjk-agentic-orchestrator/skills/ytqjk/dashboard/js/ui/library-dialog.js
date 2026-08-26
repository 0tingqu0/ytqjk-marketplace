import { api } from "../api.js";
import { byId, clear, formatBytes, formatTime, text } from "./dom.js";

function appendIndexFiles(files, heading) {
  if (!files.length) return;
  const target = byId("project-library-files");
  target.append(text("h3", heading));
  files.forEach((chunks) => {
    const item = document.createElement("details");
    item.append(text("summary", `${chunks[0].path} · ${chunks.length} 分块`));
    const parts = text("div", "", "project-chunks");
    chunks.forEach((chunk) => {
      const part = document.createElement("article");
      part.append(
        text(
          "small",
          `第 ${chunk.line_start}-${chunk.line_end} 行`,
        ),
        text("pre", chunk.content),
      );
      parts.append(part);
    });
    item.append(parts);
    target.append(item);
  });
}

function open(kicker, title, meta) {
  byId("library-kicker").textContent = kicker;
  byId("project-library-title").textContent = title;
  byId("project-library-meta").textContent = meta;
  byId("project-library-empty").hidden = true;
  clear(byId("project-library-files"));
  byId("project-library-dialog").showModal();
}

export async function showGlobalLibrary() {
  open("总库", "总库知识索引", "读取总库索引…");
  try {
    const library = await api.globalLibrary();
    const files = `${library.file_count}/${library.expected_files}`;
    const chunks = `${library.chunk_count}/${library.expected_chunks}`;
    byId("project-library-meta").textContent =
      `知识索引 ${files} 文件 · ${chunks} 分块；`
      + `索引于 ${formatTime(library.indexed_at)}`;
    byId("project-library-empty").hidden = library.files.length > 0;
    appendIndexFiles(library.files, "已验证与已批准知识");
  } catch (error) { byId("project-library-meta").textContent = error.message; }
}

export async function showProjectLibrary(project) {
  open("项目子库", project.name, "读取项目索引…");
  try {
    const library = await api.projectLibrary(project.id);
    const cache = library.cache;
    const files = `${library.file_count}/${library.expected_files}`;
    const chunks = `${library.chunk_count}/${library.expected_chunks}`;
    const used = formatBytes(cache.used_bytes);
    byId("project-library-meta").textContent =
      `知识缓存 ${library.prefetch.length} 分块 · ${used}；`
      + `源码索引 ${files} 文件 · ${chunks} 分块；${cache.policy}`;
    byId("project-library-empty").hidden =
      library.files.length > 0 || library.prefetch.length > 0;
    const target = byId("project-library-files");
    if (library.prefetch.length) {
      target.append(text("h3", "总库预取缓存"));
      library.prefetch.forEach((entry) => {
        target.append(text(
          "pre",
          `命中 ${entry.hit_count} 次 · ${entry.path}\n${entry.content}`,
          "project-knowledge",
        ));
      });
    }
    appendIndexFiles(library.files, "项目源码索引");
  } catch (error) { byId("project-library-meta").textContent = error.message; }
}

export function bindLibraryDialog() {
  byId("close-project-library").onclick = () => {
    byId("project-library-dialog").close();
  };
  byId("project-library-dialog").onclick = (event) => {
    if (event.target === event.currentTarget) event.currentTarget.close();
  };
}
