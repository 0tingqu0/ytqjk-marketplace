from __future__ import annotations

import hashlib
import re
from pathlib import Path


KNOWLEDGE_ROOTS = (
    "verified",
    "personal-experience/approved",
    "error-experience/approved",
    "personal-experience/candidates",
    "error-experience/candidates",
)
RAW_CONTENT_MARKER = "## 原始资料\n\n"


def find_duplicate(root: Path, content: str, source: bytes) -> str | None:
    content_hash = content_digest(content)
    for path in _knowledge_documents(root):
        try:
            existing = path.read_text(encoding="utf-8")
        except OSError:
            continue
        if content_digest(raw_content(existing)) == content_hash:
            return path.relative_to(root).as_posix()
    source_hash = hashlib.sha256(source).digest()
    originals = root / "personal-experience/candidates/imports/originals"
    if originals.is_dir():
        for path in originals.iterdir():
            if path.is_file() and hashlib.sha256(path.read_bytes()).digest() == source_hash:
                return path.relative_to(root).as_posix()
    return None


def content_digest(content: str) -> bytes:
    normalized = re.sub(r"\s+", " ", content).strip()
    return hashlib.sha256(normalized.encode("utf-8")).digest()


def raw_content(document: str) -> str:
    return document.partition(RAW_CONTENT_MARKER)[2] if RAW_CONTENT_MARKER in document else document


def _knowledge_documents(root: Path) -> list[Path]:
    return [
        path
        for relative in KNOWLEDGE_ROOTS
        if (directory := root / relative).is_dir()
        for path in directory.rglob("*.md")
    ]
