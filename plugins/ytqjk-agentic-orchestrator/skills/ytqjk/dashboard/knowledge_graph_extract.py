"""Deterministic high-confidence entity and relation extraction."""

from __future__ import annotations

import re
import unicodedata
from collections import Counter


WIKI = re.compile(r"\[\[([^\]|#]{2,80})(?:[|#][^\]]*)?\]\]")
CODE = re.compile(r"`([^`\n]{2,80})`")
HEADING = re.compile(r"^\s{0,3}#{1,6}\s+(.{2,100}?)\s*$")
ENGLISH_TERM = re.compile(
    r"\b(?:[A-Z][A-Za-z0-9]+|[a-z]+[A-Z][A-Za-z0-9]*)"
    r"(?:[ ._-](?:[A-Z][A-Za-z0-9]+|[0-9]+)){0,3}\b"
)
TECH_TERM = re.compile(
    r"[\u4e00-\u9fffA-Za-z0-9]{0,12}?"
    r"(?:知识图谱|知识库|语义搜索|语义检索|实体关系抽取|关系抽取|"
    r"向量索引|全文索引|相似知识推荐|知识推荐|路径探索|工作台|"
    r"接口|服务|模型|算法|模块|数据库)"
)
RELATION_WORDS = {
    "使用": ("uses", "使用"),
    "依赖": ("depends_on", "依赖"),
    "属于": ("belongs_to", "属于"),
    "包含": ("contains", "包含"),
    "支持": ("supports", "支持"),
    "关联": ("related_to", "关联"),
    "连接": ("connects_to", "连接"),
    "引用": ("references", "引用"),
    "导致": ("causes", "导致"),
    "生成": ("produces", "生成"),
    "基于": ("based_on", "基于"),
    "调用": ("calls", "调用"),
}
GENERIC = {
    "http", "https", "true", "false", "none", "null", "string",
    "number", "object", "array", "markdown", "json", "python",
    "path", "valueerror", "runtimeerror", "oserror", "error", "exception",
    "return", "any", "callable", "connection", "row", "text", "bytesio",
    "where", "from", "select", "values", "insert into", "join", "order by",
    "and", "or", "on", "set", "read", "run", "use", "before", "after",
    "active", "blocked", "done", "failed", "running", "succeeded",
    "id", "head", "status", "update", "approved", "verified", "candidate",
    "applied", "all",
    "testcase", "assertequal", "asserttrue", "assertfalse", "assertin",
    "assertnotin", "assertraisesregex", "temporarydirectory", "the", "never",
    "users", "scripts", "license", "e402", "utf-8", "content-type",
    "gib", "mib", "localappdata", "not_configured", "installed",
}


def canonical_label(value: str) -> str:
    normalized = unicodedata.normalize("NFKC", value).strip()
    normalized = re.sub(r"^[#*\-\s]+|[，。；：:,.!?！？\s]+$", "", normalized)
    return re.sub(r"\s+", " ", normalized)[:80]


def semantic_tokens(value: str) -> Counter[str]:
    normalized = unicodedata.normalize("NFKC", value).lower()
    tokens = re.findall(r"[a-z][a-z0-9_.+-]{1,}|[\u4e00-\u9fff]+", normalized)
    result: Counter[str] = Counter()
    for token in tokens:
        if re.fullmatch(r"[\u4e00-\u9fff]+", token):
            if 2 <= len(token) <= 8:
                result[token] += 2
            for size in (2, 3):
                for index in range(max(0, len(token) - size + 1)):
                    result[token[index:index + size]] += 1
        elif token not in GENERIC:
            result[token] += 1
    return result


def _span_rows(line: str) -> list[dict[str, object]]:
    rows: list[dict[str, object]] = []
    patterns = (
        (WIKI, "concept", 0.98),
        (CODE, "term", 0.9),
        (TECH_TERM, "concept", 0.78),
        (ENGLISH_TERM, "term", 0.72),
    )
    heading = HEADING.match(line)
    if heading:
        rows.append({
            "label": canonical_label(heading.group(1)),
            "kind": "topic", "confidence": 0.94,
            "start": heading.start(1), "end": heading.end(1),
        })
    for pattern, kind, confidence in patterns:
        for match in pattern.finditer(line):
            label = canonical_label(match.group(1) if match.lastindex else match.group(0))
            if not 2 <= len(label) <= 80 or label.casefold() in GENERIC:
                continue
            rows.append({
                "label": label, "kind": kind, "confidence": confidence,
                "start": match.start(), "end": match.end(),
            })
    deduped: dict[str, dict[str, object]] = {}
    for row in rows:
        key = str(row["label"]).casefold()
        current = deduped.get(key)
        if current is None or float(row["confidence"]) > float(current["confidence"]):
            deduped[key] = row
    return sorted(deduped.values(), key=lambda row: int(row["start"]))


def extract_knowledge(content: str, line_offset: int = 0) -> dict[str, object]:
    entities: list[dict[str, object]] = []
    relations: list[dict[str, object]] = []
    frontmatter = False
    for index, line in enumerate(content.splitlines(), start=1 + line_offset):
        if index == 1 and line.strip() == "---":
            frontmatter = True
            continue
        if frontmatter:
            if line.strip() == "---":
                frontmatter = False
            continue
        spans = _span_rows(line)
        for row in spans:
            entities.append({**row, "line": index})
        for word, (relation_type, label) in RELATION_WORDS.items():
            for match in re.finditer(word, line):
                left = [row for row in spans if int(row["end"]) <= match.start()]
                right = [row for row in spans if int(row["start"]) >= match.end()]
                if not left or not right:
                    continue
                source, target = left[-1], right[0]
                if source["label"] == target["label"]:
                    continue
                relations.append({
                    "source": source["label"], "target": target["label"],
                    "type": relation_type, "label": label,
                    "confidence": min(
                        float(source["confidence"]),
                        float(target["confidence"]),
                    ),
                    "line": index, "excerpt": line.strip()[:240],
                })
    return {"entities": entities, "relations": relations}


__all__ = [
    "canonical_label", "extract_knowledge", "semantic_tokens",
]
