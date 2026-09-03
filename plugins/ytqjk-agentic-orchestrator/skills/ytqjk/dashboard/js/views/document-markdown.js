const METADATA_LABELS = Object.freeze({
  approval: "批准方式",
  approved_at: "批准时间",
  intake_id: "投递编号",
  original_name: "原文件名",
  original_path: "原文件路径",
  received_at: "接收时间",
  source: "来源",
  status: "状态",
});

function normalizeLines(source) {
  return String(source ?? "").replace(/\r\n?/g, "\n").split("\n");
}

function stripYamlQuotes(value) {
  const first = value.at(0);
  const last = value.at(-1);
  return first && first === last && ["\"", "'"].includes(first)
    ? value.slice(1, -1)
    : value;
}

function parseFrontmatter(lines) {
  if (lines[0]?.trim() !== "---") return { bodyStart: 0, metadata: [] };
  const closing = lines.findIndex(
    (line, index) => index > 0 && ["---", "..."].includes(line.trim()),
  );
  if (closing < 0) return { bodyStart: 0, metadata: [] };
  const metadata = lines.slice(1, closing).flatMap((line) => {
    const match = line.match(/^\s*([^:#][^:]*?):\s*(.*?)\s*$/);
    if (!match) return [];
    return [{ key: match[1].trim(), value: stripYamlQuotes(match[2]) }];
  });
  return { bodyStart: closing + 1, metadata };
}

function heading(line) {
  const match = line.match(/^\s{0,3}(#{1,6})[ \t]+(.+?)(?:[ \t]+#+[ \t]*)?$/);
  return match
    ? { type: "heading", level: match[1].length, text: match[2].trimEnd() }
    : null;
}

function listItem(line) {
  const unordered = line.match(/^\s{0,3}[-+*]\s+(.+)$/);
  if (unordered) return { ordered: false, text: unordered[1] };
  const ordered = line.match(/^\s{0,3}\d+[.)]\s+(.+)$/);
  return ordered ? { ordered: true, text: ordered[1] } : null;
}

function isBlockStart(line) {
  return Boolean(
    heading(line)
    || listItem(line)
    || /^\s{0,3}>/.test(line)
    || /^\s{0,3}```/.test(line),
  );
}

export function parseDocumentMarkdown(source) {
  const lines = normalizeLines(source);
  const { bodyStart, metadata } = parseFrontmatter(lines);
  const blocks = [];
  let index = bodyStart;
  while (index < lines.length) {
    const line = lines[index];
    if (!line.trim()) { index += 1; continue; }
    const fence = line.match(/^\s{0,3}```\s*([^\s`]*)\s*$/);
    if (fence) {
      const code = [];
      index += 1;
      while (index < lines.length && !/^\s{0,3}```\s*$/.test(lines[index])) {
        code.push(lines[index]);
        index += 1;
      }
      if (index < lines.length) index += 1;
      blocks.push({ type: "code", language: fence[1], text: code.join("\n") });
      continue;
    }
    const title = heading(line);
    if (title) { blocks.push(title); index += 1; continue; }
    if (/^\s{0,3}>/.test(line)) {
      const quote = [];
      while (index < lines.length && /^\s{0,3}>/.test(lines[index])) {
        quote.push(lines[index].replace(/^\s{0,3}>\s?/, ""));
        index += 1;
      }
      blocks.push({ type: "blockquote", text: quote.join("\n") });
      continue;
    }
    const firstItem = listItem(line);
    if (firstItem) {
      const items = [];
      while (index < lines.length) {
        const item = listItem(lines[index]);
        if (!item || item.ordered !== firstItem.ordered) break;
        items.push(item.text);
        index += 1;
      }
      blocks.push({ type: "list", ordered: firstItem.ordered, items });
      continue;
    }
    const paragraph = [];
    while (
      index < lines.length
      && lines[index].trim()
      && (!paragraph.length || !isBlockStart(lines[index]))
    ) {
      paragraph.push(lines[index].trim());
      index += 1;
    }
    blocks.push({ type: "paragraph", text: paragraph.join(" ") });
  }
  return { metadata, blocks };
}

function textNode(tag, value, className = "") {
  const node = document.createElement(tag);
  if (className) node.className = className;
  node.textContent = value;
  return node;
}

function inlineNode(tag, value) {
  const node = document.createElement(tag);
  let cursor = 0;
  for (const match of String(value).matchAll(/`([^`\n]+)`/g)) {
    node.append(document.createTextNode(value.slice(cursor, match.index)));
    node.append(textNode("code", match[1]));
    cursor = match.index + match[0].length;
  }
  node.append(document.createTextNode(value.slice(cursor)));
  return node;
}

function renderMetadata(metadata) {
  const section = document.createElement("section");
  section.className = "document-metadata";
  section.setAttribute("aria-label", "文档元数据");
  const list = document.createElement("dl");
  metadata.forEach(({ key, value }) => {
    list.append(
      textNode("dt", METADATA_LABELS[key] || key),
      textNode("dd", value || "—"),
    );
  });
  section.append(list);
  return section;
}

function renderBlock(block) {
  if (block.type === "heading") {
    return inlineNode(`h${Math.min(6, block.level + 2)}`, block.text);
  }
  if (block.type === "list") {
    const list = document.createElement(block.ordered ? "ol" : "ul");
    block.items.forEach((item) => list.append(inlineNode("li", item)));
    return list;
  }
  if (block.type === "blockquote") return inlineNode("blockquote", block.text);
  if (block.type === "code") {
    const pre = document.createElement("pre");
    const code = textNode("code", block.text);
    if (block.language) code.dataset.language = block.language;
    pre.append(code);
    return pre;
  }
  return inlineNode("p", block.text);
}

export function renderDocumentMarkdown(target, source) {
  const parsed = parseDocumentMarkdown(source);
  const body = document.createElement("section");
  body.className = "document-prose";
  if (parsed.blocks.length) body.append(...parsed.blocks.map(renderBlock));
  else body.append(textNode("p", "这份文档暂无正文。", "muted"));
  target.replaceChildren(
    ...(parsed.metadata.length ? [renderMetadata(parsed.metadata)] : []),
    body,
  );
}
