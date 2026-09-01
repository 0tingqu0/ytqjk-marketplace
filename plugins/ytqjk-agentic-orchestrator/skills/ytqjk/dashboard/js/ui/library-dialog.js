import { api } from "../api.js";
import { byId, clear, formatBytes, text } from "./dom.js";

function groupChunks(chunks) {
	const files = new Map();
	chunks.forEach((chunk) => {
		const parts = files.get(chunk.path) || [];
		parts.push(chunk);
		files.set(chunk.path, parts);
	});
	return [...files.values()];
}

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
				`第 ${chunk.start}-${chunk.end} 字符`,
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
	open("总库", "总库资料索引", "读取总库索引…");
	try {
		const library = await api.globalLibrary();
		byId("project-library-meta").textContent =
			`当前载入 ${library.count} 分块作为资料预览`;
		byId("project-library-empty").hidden = library.count > 0;
		appendIndexFiles(groupChunks(library.chunks), "已验证与已批准资料");
	} catch (error) { byId("project-library-meta").textContent = error.message; }
}

export async function showProjectLibrary(project, node) {
	open("项目子库", project.name, "读取项目索引…");
	try {
		const library = await api.projectLibrary(project.id);
		const stats = node.stats;
		byId("project-library-meta").textContent =
			`资料索引 ${stats.indexed_documents}/${stats.total_documents} 文件 · `
			+ `${stats.indexed_chunks}/${stats.total_chunks} 分块；`
			+ `占用 ${formatBytes(stats.used_bytes)}/${formatBytes(node.capacity_bytes)}；`
			+ `当前载入 ${library.count} 分块作为资料预览`;
		byId("project-library-empty").hidden = library.count > 0;
		appendIndexFiles(groupChunks(library.chunks), "项目资料预览");
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
