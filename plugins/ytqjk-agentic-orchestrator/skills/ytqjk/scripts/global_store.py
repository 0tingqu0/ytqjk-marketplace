from __future__ import annotations

import hashlib
from pathlib import Path
from typing import Any

from rag_common import Chunk, is_indexable, read_text, split_chunks
from rag_security import contains_high_confidence_secret


APPROVED_ROOTS = (
    "verified",
    "personal-experience/approved",
    "error-experience/approved",
)


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
        source_root = knowledge_root / relative_root
        if not source_root.exists():
            continue
        for full_path in sorted(source_root.rglob("*")):
            if not full_path.is_file():
                continue
            relative = full_path.relative_to(knowledge_root).as_posix()
            if not is_indexable(
                relative, full_path, int(index_config["max_file_bytes"])
            ):
                skipped += 1
                continue
            text = read_text(full_path)
            if text is None:
                skipped += 1
                continue
            if contains_high_confidence_secret(text):
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
