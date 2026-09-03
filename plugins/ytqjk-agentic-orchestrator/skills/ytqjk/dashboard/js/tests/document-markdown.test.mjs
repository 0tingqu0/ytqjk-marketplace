import assert from "node:assert/strict";
import test from "node:test";

import { parseDocumentMarkdown } from "../views/document-markdown.js";

test("document markdown separates YAML frontmatter from the body", () => {
  const result = parseDocumentMarkdown(`---
title: "知识拓扑"
status: candidate
tags:
---
# 说明

正文。`);

  assert.deepEqual(result.metadata, [
    { key: "title", value: "知识拓扑" },
    { key: "status", value: "candidate" },
    { key: "tags", value: "" },
  ]);
  assert.deepEqual(result.blocks, [
    { type: "heading", level: 1, text: "说明" },
    { type: "paragraph", text: "正文。" },
  ]);
});

test("document markdown parses the supported reading blocks", () => {
  const result = parseDocumentMarkdown(`# 标题

第一行
第二行

- 节点
- 关系

1. 读取
2. 校验

> 保留来源
> 避免猜测

\`\`\`json
{"safe":"<script>"}
\`\`\``);

  assert.deepEqual(result.blocks, [
    { type: "heading", level: 1, text: "标题" },
    { type: "paragraph", text: "第一行 第二行" },
    { type: "list", ordered: false, items: ["节点", "关系"] },
    { type: "list", ordered: true, items: ["读取", "校验"] },
    { type: "blockquote", text: "保留来源\n避免猜测" },
    { type: "code", language: "json", text: '{"safe":"<script>"}' },
  ]);
});

test("unterminated frontmatter and unknown syntax remain readable text", () => {
  const result = parseDocumentMarkdown("---\ntitle: unfinished\n**literal**");

  assert.deepEqual(result.metadata, []);
  assert.deepEqual(result.blocks, [
    { type: "paragraph", text: "--- title: unfinished **literal**" },
  ]);
});

test("heading keeps a meaningful trailing hash", () => {
  const result = parseDocumentMarkdown("# C#\n## 标题 ##");

  assert.deepEqual(result.blocks, [
    { type: "heading", level: 1, text: "C#" },
    { type: "heading", level: 2, text: "标题" },
  ]);
});
