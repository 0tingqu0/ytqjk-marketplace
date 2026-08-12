from __future__ import annotations

import re
from pathlib import Path


MAX_CHUNK_CHARS = 1800
HEADING = re.compile(r"^#{1,6}\s+(.+)$", re.MULTILINE)


def split_knowledge(content: str) -> list[dict[str, str]]:
    if not content.strip():
        return []
    chunks = []
    heading = "未命名片段"
    current = ""
    for part in re.split(r"(#{1,6}\s+.+\n)", content):
        match = HEADING.match(part.strip())
        if match:
            chunks.extend(_split_long(current, heading))
            heading, current = match.group(1).strip(), part
            continue
        current += part
    chunks.extend(_split_long(current, heading))
    return [
        {"title": title, "summary": _summary(text), "content": text.strip()}
        for title, text in chunks
        if text.strip()
    ]


def write_chunks(root: Path, intake_id: str, source_name: str, content: str) -> list[str]:
    paths = []
    for number, chunk in enumerate(split_knowledge(content), 1):
        directory = root / "personal-experience/candidates/imports/chunks" / intake_id
        directory.mkdir(parents=True, exist_ok=True)
        target = directory / f"{number:03d}.md"
        target.write_text(
            "---\nstatus: CANDIDATE\nsource: dashboard-intake-chunk\n"
            f"intake_id: {intake_id}\nsource_name: {source_name}\nchunk_number: {number}\n---\n\n"
            f"# {chunk['title']}\n\n## 片段分析\n\n"
            f"- 摘要：{chunk['summary']}\n- 来源资料：`{source_name}`\n"
            f"- 片段序号：{number}\n\n## 知识片段\n\n{chunk['content']}\n",
            encoding="utf-8",
        )
        paths.append(target.relative_to(root).as_posix())
    return paths


def _split_long(content: str, heading: str) -> list[tuple[str, str]]:
    pieces = []
    current = ""
    for paragraph in re.split(r"(\n\s*\n)", content):
        if len(current) + len(paragraph) > MAX_CHUNK_CHARS and current.strip():
            pieces.append((heading, current))
            current = ""
        while len(paragraph) > MAX_CHUNK_CHARS:
            pieces.append((heading, paragraph[:MAX_CHUNK_CHARS]))
            paragraph = paragraph[MAX_CHUNK_CHARS:]
        current += paragraph
    if current.strip():
        pieces.append((heading, current))
    return pieces


def _summary(content: str) -> str:
    return " ".join(content.split())[:240]
