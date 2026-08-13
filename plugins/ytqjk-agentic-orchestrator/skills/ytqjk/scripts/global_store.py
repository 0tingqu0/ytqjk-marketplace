from __future__ import annotations

import hashlib
from pathlib import Path
from pathlib import PurePosixPath
from typing import Any, Mapping

from rag_common import Chunk, is_indexable, read_text, split_chunks
from rag_security import contains_high_confidence_secret


APPROVED_ROOTS = (
    "verified",
    "personal-experience/approved",
    "error-experience/approved",
)
APPROVED_PREFIXES = tuple(PurePosixPath(value) for value in APPROVED_ROOTS)


def is_approved_path(value: str) -> bool:
    path = PurePosixPath(value.replace("\\", "/"))
    if path.is_absolute() or ".." in path.parts:
        return False
    return any(path.is_relative_to(prefix) for prefix in APPROVED_PREFIXES)


def _safe_approved_source(
    knowledge_root: Path, relative: PurePosixPath
) -> Path | None:
    source = knowledge_root
    try:
        resolved_root = knowledge_root.resolve(strict=True)
        for part in relative.parts:
            source /= part
            if source.is_symlink():
                return None
        resolved_source = source.resolve(strict=True)
        if not resolved_source.is_relative_to(resolved_root):
            return None
    except (OSError, RuntimeError):
        return None
    return source


def is_current_approved_hit(
    knowledge_root: Path, row: Mapping[str, object]
) -> bool:
    value = str(row.get("path", ""))
    if not is_approved_path(value):
        return False
    relative = PurePosixPath(value.replace("\\", "/"))
    source = _safe_approved_source(knowledge_root, relative)
    if source is None:
        return False
    try:
        text = read_text(source)
    except (OSError, RuntimeError):
        return False
    if text is None or contains_high_confidence_secret(text):
        return False
    source_hash = row.get("source_sha256")
    if source_hash and source_hash != hashlib.sha256(text.encode("utf-8")).hexdigest():
        return False
    try:
        line_start = int(row["line_start"])
        line_end = int(row["line_end"])
    except (KeyError, TypeError, ValueError):
        return False
    lines = text.splitlines()
    if not 1 <= line_start <= line_end <= len(lines):
        return False
    cited = "\n".join(lines[line_start - 1:line_end]).strip()
    return cited == str(row.get("content", ""))


def chunks_fingerprint(chunks: list[Chunk]) -> str:
    source = "\0".join(
        f"{chunk.path}\0{chunk.source_sha256}" for chunk in chunks
    ).encode("utf-8")
    return hashlib.sha256(source).hexdigest()


def scan_global(
    knowledge_root: Path, config: dict[str, Any]
) -> tuple[list[Chunk], dict[str, int]]:
    index_config = config["index"]
    chunks: list[Chunk] = []
    text_bytes = 0
    files = 0
    skipped = 0
    for relative_root in APPROVED_ROOTS:
        source_root = _safe_approved_source(
            knowledge_root, PurePosixPath(relative_root)
        )
        if source_root is None:
            continue
        try:
            paths = sorted(source_root.rglob("*"))
        except (OSError, RuntimeError):
            skipped += 1
            continue
        for full_path in paths:
            try:
                relative = full_path.relative_to(knowledge_root).as_posix()
                safe_path = _safe_approved_source(
                    knowledge_root, PurePosixPath(relative)
                )
                if safe_path is None or not safe_path.is_file():
                    continue
                if not is_indexable(
                    relative, safe_path, int(index_config["max_file_bytes"])
                ):
                    skipped += 1
                    continue
                text = read_text(safe_path)
                if text is None:
                    skipped += 1
                    continue
                if contains_high_confidence_secret(text):
                    skipped += 1
                    continue
            except (OSError, RuntimeError, ValueError):
                skipped += 1
                continue
            files += 1
            text_bytes += len(text.encode("utf-8"))
            chunks.extend(
                split_chunks(
                    relative,
                    text,
                    "GLOBAL",
                    int(index_config["chunk_chars"]),
                    int(index_config["overlap_chars"]),
                )
            )
    return chunks, {
        "files": files,
        "skipped": skipped,
        "text_bytes": text_bytes,
        "chunks": len(chunks),
    }
