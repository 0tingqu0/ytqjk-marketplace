import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const html = readFileSync(new URL("../../index.html", import.meta.url), "utf8");
const dialog = readFileSync(
	new URL("../ui/tree-dialog.js", import.meta.url),
	"utf8",
);
const libraries = readFileSync(
	new URL("../views/libraries.js", import.meta.url),
	"utf8",
);
const libraryDialog = readFileSync(
	new URL("../ui/library-dialog.js", import.meta.url),
	"utf8",
);

test("Library create sends canonical capacity_bytes", () => {
  assert.match(html, /id="tree-capacity-mib"[^>]*type="number"/);
  assert.match(html, /min="64" max="1048576" step="64" value="1024"/);
  assert.match(dialog, /field\("tree-capacity-field", creating\)/);
  assert.match(
    dialog,
    /capacity_bytes:\s*Number\(byId\("tree-capacity-mib"\)\.value\)\s*\*\s*1024\s*\*\s*1024/,
  );
});

test("Library views consume canonical Node stats", () => {
	for (const field of [
		"used_bytes",
		"indexed_documents",
		"total_documents",
		"indexed_chunks",
		"total_chunks",
	]) {
		assert.match(libraries, new RegExp(`stats\\.${field}`));
	}
	assert.match(libraries, /node\.capacity_bytes/);
	assert.doesNotMatch(libraries, /node\.index|cache\.entries|rebuild_index/);
});

test("Library detail uses unified material terminology", () => {
	assert.match(libraryDialog, /资料索引/);
	assert.match(libraryDialog, /当前载入.*资料预览/);
	assert.doesNotMatch(libraryDialog, /知识缓存|源码索引|预取缓存/);
});
