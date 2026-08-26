from __future__ import annotations

import hashlib
import re
from dataclasses import dataclass
from pathlib import Path
from pathlib import PurePosixPath
from typing import Any, Mapping

from path_safety import is_reparse
from rag_common import Chunk, is_indexable, read_text, split_chunks
from rag_security import contains_high_confidence_secret


APPROVED_ROOTS = (
    "verified",
    "personal-experience/approved",
    "error-experience/approved",
)
APPROVED_PREFIXES = tuple(PurePosixPath(value) for value in APPROVED_ROOTS)
_SHA256 = re.compile(r"^[0-9a-f]{64}$")


@dataclass(frozen=True)
class ApprovedSource:
    document_id: str
    path: str
    source_sha256: str
    text: str


def is_approved_path(value: str) -> bool:
    path = PurePosixPath(value.replace("\\", "/"))
    if path.is_absolute() or ".." in path.parts:
        return False
    return (
        value == path.as_posix()
        and any(
            path.is_relative_to(prefix)
            for prefix in APPROVED_PREFIXES
        )
    )


def _safe_approved_source(
    knowledge_root: Path, relative: PurePosixPath
) -> Path | None:
    source = knowledge_root
    try:
        if is_reparse(source):
            return None
        resolved_root = knowledge_root.resolve(strict=True)
        for part in relative.parts:
            source /= part
            if is_reparse(source):
                return None
        resolved_source = source.resolve(strict=True)
        if not resolved_source.is_relative_to(resolved_root):
            return None
    except (OSError, RuntimeError):
        return None
    return source


def load_approved_source(
    knowledge_root: Path,
    value: str,
) -> ApprovedSource | None:
    if not is_approved_path(value):
        return None
    relative = PurePosixPath(value)
    source = _safe_approved_source(knowledge_root, relative)
    if source is None or not source.is_file():
        return None
    try:
        text = read_text(source)
    except (OSError, RuntimeError):
        return None
    if text is None or contains_high_confidence_secret(text):
        return None
    digest = hashlib.sha256(text.encode("utf-8")).hexdigest()
    identifier = hashlib.sha256(value.encode("utf-8")).hexdigest()
    return ApprovedSource(identifier, value, digest, text)


def approved_hit_provenance(
    knowledge_root: Path,
    row: Mapping[str, object],
) -> dict[str, object] | None:
    if "source_sha256" not in row:
        return None
    captured = capture_approved_hit(knowledge_root, row)
    if captured is None:
        return None
    return {
        "document_id": approved_document_id(str(captured["path"])),
        "path": captured["path"],
        "source_sha256": captured["source_sha256"],
        "line_start": captured["line_start"],
        "line_end": captured["line_end"],
    }


def capture_approved_hit(
    knowledge_root: Path,
    row: Mapping[str, object],
) -> dict[str, object] | None:
    value = row.get("path")
    if type(value) is not str:
        return None
    source = load_approved_source(knowledge_root, value)
    if source is None:
        return None
    source_hash = row.get("source_sha256")
    if (
        source_hash is not None
        and (
            type(source_hash) is not str
            or _SHA256.fullmatch(source_hash) is None
            or source_hash != source.source_sha256
        )
    ):
        return None
    line_start = row.get("line_start")
    line_end = row.get("line_end")
    if type(line_start) is not int or type(line_end) is not int:
        return None
    lines = source.text.splitlines()
    if not 1 <= line_start <= line_end <= len(lines):
        return None
    cited = "\n".join(lines[line_start - 1:line_end]).strip()
    if cited != str(row.get("content", "")):
        return None
    captured = dict(row)
    captured.update({
        "path": source.path,
        "source_sha256": source.source_sha256,
        "line_start": line_start,
        "line_end": line_end,
    })
    return captured


def is_current_approved_hit(
    knowledge_root: Path, row: Mapping[str, object]
) -> bool:
    return approved_hit_provenance(knowledge_root, row) is not None


def retain_current_approved_hits(
    knowledge_root: Path,
    rows: list[dict[str, Any]],
) -> list[dict[str, Any]]:
    return [
        row for row in rows
        if is_current_approved_hit(knowledge_root, row)
    ]


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


def approved_document_id(path: str) -> str:
    if not is_approved_path(path):
        raise ValueError("UNAPPROVED_SOURCE_PATH")
    return hashlib.sha256(path.encode("utf-8")).hexdigest()
