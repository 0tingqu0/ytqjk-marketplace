"""Persist structured candidates and their searchable chunks safely."""

from __future__ import annotations

import hashlib
import json
import re
import uuid
from dataclasses import dataclass
from pathlib import Path

from candidate_bundle import (
    CandidateBundleError,
    candidate_lifecycle_lock,
)
from candidate_file_safety import CandidateFileError, atomic_replace_file
from candidate_file_safety import read_file_snapshot
from path_safety import is_reparse


_DIGEST = re.compile(r"^[0-9a-f]{64}$")
_SUFFIXES = frozenset((
    ".bmp", ".jpeg", ".jpg", ".pdf", ".png", ".tif", ".tiff",
    ".webp",
))
_MAX_CHUNKS = 10_000
_MAX_CHUNK_BYTES = 1024 * 1024


class StructuredCandidateWriteError(RuntimeError):
    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


@dataclass(frozen=True)
class WriteResult:
    value: dict[str, object]
    created: tuple[Path, ...]


def write_structured_candidate(
    root: Path,
    intake_id: str,
    candidate: dict[str, object],
    source: bytes,
    source_name: str,
) -> WriteResult:
    candidate_id = str(candidate.get("candidate_id", ""))
    if not _DIGEST.fullmatch(candidate_id):
        raise StructuredCandidateWriteError("CANDIDATE_ID_INVALID")
    document = root / "personal-experience" / "candidates" \
        / "imports" / "structured" / f"{candidate_id}.md"
    try:
        with candidate_lifecycle_lock(root, document):
            return _write_candidate_unlocked(
                root, intake_id, candidate, source, source_name,
            )
    except StructuredCandidateWriteError:
        raise
    except (CandidateBundleError, CandidateFileError) as error:
        raise StructuredCandidateWriteError(
            "CANDIDATE_LOCK_FAILED"
        ) from error


def _write_candidate_unlocked(
    root: Path,
    intake_id: str,
    candidate: dict[str, object],
    source: bytes,
    source_name: str,
) -> WriteResult:
    created: list[Path] = []
    try:
        uuid.UUID(intake_id)
        candidate_id = str(candidate["candidate_id"])
        source_digest = hashlib.sha256(source).hexdigest()
        suffix = Path(source_name).suffix.casefold()
        if not _DIGEST.fullmatch(candidate_id):
            raise StructuredCandidateWriteError("CANDIDATE_ID_INVALID")
        if candidate.get("source_digest") != source_digest:
            raise StructuredCandidateWriteError("SOURCE_INVALID")
        if suffix not in _SUFFIXES:
            raise StructuredCandidateWriteError("SOURCE_INVALID")
        base = root / "personal-experience" / "candidates" \
            / "imports" / "structured"
        _prepare_directory(root, base)
        _prepare_directory(root, base / "originals")
        document = base / f"{candidate_id}.md"
        detail = base / f"{candidate_id}.json"
        original = base / "originals" / f"{candidate_id}{suffix}"
        chunk_dir = root / "personal-experience" / "candidates" \
            / "imports" / "chunks" / intake_id
        chunks = _chunk_contents(intake_id, candidate_id, candidate)
        chunk_paths = tuple(
            chunk_dir / f"{number:03d}.md"
            for number in range(1, len(chunks) + 1)
        )
        metadata = candidate["metadata"]
        document_ref = document.relative_to(root).as_posix()
        detail_ref = detail.relative_to(root).as_posix()
        original_ref = original.relative_to(root).as_posix()
        markdown = _document(
            intake_id, candidate_id, source_digest, original_ref,
            detail_ref, len(chunks), metadata,
        )
        for path, content in (
            (original, source),
            (document, markdown.encode("utf-8")),
            (detail, _json_bytes(candidate)),
        ):
            if _write_once(root, path, content):
                created.append(path)
        if chunks:
            directory_created = _prepare_directory(root, chunk_dir)
            if directory_created:
                created.append(chunk_dir)
            expected = {path.name for path in chunk_paths}
            if {item.name for item in chunk_dir.iterdir()} - expected:
                raise StructuredCandidateWriteError(
                    "DUPLICATE_CANDIDATE"
                )
            for path, content in zip(chunk_paths, chunks, strict=True):
                if _write_once(root, path, content):
                    created.append(path)
        return WriteResult({
            "candidate_path": document_ref,
            "detail_path": detail_ref,
            "original_path": original_ref,
            "chunk_paths": [path.relative_to(root).as_posix()
                            for path in chunk_paths],
            "source_sha256": source_digest,
            "candidate": candidate,
        }, tuple(created))
    except StructuredCandidateWriteError:
        cleanup_artifacts(tuple(created))
        raise
    except (CandidateFileError, OSError, TypeError, ValueError) as error:
        cleanup_artifacts(tuple(created))
        raise StructuredCandidateWriteError(
            "STRUCTURED_CANDIDATE_WRITE_FAILED"
        ) from error


def cleanup_artifacts(paths: tuple[Path, ...]) -> None:
    for path in paths:
        if not path.is_dir():
            path.unlink(missing_ok=True)
    for path in reversed(paths):
        if path.is_dir():
            path.rmdir()


def promote_chunk_bytes(
    content: bytes, approved_at: str, approval: str,
) -> bytes:
    try:
        text = content.decode("utf-8")
    except UnicodeError as error:
        raise StructuredCandidateWriteError("CHUNK_ENCODING_INVALID") \
            from error
    marker = "source: structured-intake-chunk"
    text = text.replace("\r\n", "\n").replace("\r", "\n")
    if marker not in text:
        return content
    end = text.find("\n---\n", 4)
    if (
        not text.startswith("---\n")
        or end < 0
        or "status: CANDIDATE\n" not in text[:end]
    ):
        raise StructuredCandidateWriteError("CHUNK_GOVERNANCE_INVALID")
    header = text[:end].replace(
        "status: CANDIDATE\n", "status: APPROVED\n", 1,
    )
    metadata = f"\napproved_at: {approved_at}\napproval: {approval}"
    text = header + metadata + text[end:]
    return text.encode("utf-8")


def _chunk_contents(
    intake_id: str,
    candidate_id: str,
    candidate: dict[str, object],
) -> tuple[bytes, ...]:
    values = candidate.get("chunks")
    if not isinstance(values, list) or len(values) > _MAX_CHUNKS:
        raise StructuredCandidateWriteError("CHUNKS_INVALID")
    rendered = []
    for number, value in enumerate(values, 1):
        if not isinstance(value, dict):
            raise StructuredCandidateWriteError("CHUNKS_INVALID")
        text = value.get("text")
        if not isinstance(text, str):
            raise StructuredCandidateWriteError("CHUNKS_INVALID")
        evidence = json.dumps(
            value, ensure_ascii=False, allow_nan=False,
            indent=2, sort_keys=True,
        )
        body = (
            "---\nstatus: CANDIDATE\n"
            "source: structured-intake-chunk\n"
            f"intake_id: {intake_id}\n"
            f"candidate_id: {candidate_id}\n"
            f"chunk_number: {number}\n---\n\n"
            f"# 知识片段 {number:03d}\n\n{text}\n\n"
            f"## 结构化证据\n\n```json\n{evidence}\n```\n"
        ).encode("utf-8")
        if len(body) > _MAX_CHUNK_BYTES:
            raise StructuredCandidateWriteError("CHUNK_TOO_LARGE")
        rendered.append(body)
    return tuple(rendered)


def _document(
    intake_id: str,
    candidate_id: str,
    source_digest: str,
    original_ref: str,
    detail_ref: str,
    chunk_count: int,
    metadata: object,
) -> str:
    if not isinstance(metadata, dict):
        raise StructuredCandidateWriteError("METADATA_INVALID")
    title = metadata.get("title")
    summary = metadata.get("summary")
    if not isinstance(title, str) or not isinstance(summary, str):
        raise StructuredCandidateWriteError("METADATA_INVALID")
    return (
        "---\nstatus: CANDIDATE\nsource: structured-intake\n"
        f"intake_id: {intake_id}\ncandidate_id: {candidate_id}\n"
        f"original_path: {original_ref}\ndetail_path: {detail_ref}\n"
        f"source_sha256: {source_digest}\nchunk_count: {chunk_count}\n"
        f"---\n\n# {title}\n\n{summary}\n\n"
        "该资料仅为候选，必须人工复审后才能批准。\n"
    )


def _write_once(root: Path, path: Path, content: bytes) -> bool:
    if path.exists():
        try:
            snapshot = read_file_snapshot(
                root, path, max(len(content), 1),
            )
        except CandidateFileError as error:
            if str(error) == "CANDIDATE_FILE_CHANGED":
                raise StructuredCandidateWriteError(
                    "DUPLICATE_CANDIDATE"
                ) from error
            raise
        if snapshot.content != content:
            raise StructuredCandidateWriteError("DUPLICATE_CANDIDATE")
        return False
    atomic_replace_file(root, path, content)
    return True


def _prepare_directory(root: Path, path: Path) -> bool:
    created = not path.exists()
    path.mkdir(parents=True, exist_ok=True)
    current = path.absolute()
    root = root.absolute()
    while True:
        if is_reparse(current):
            raise StructuredCandidateWriteError("UNSAFE_CHUNK_DIRECTORY")
        if current == root:
            break
        current = current.parent
    if not path.is_dir() or path.resolve().parent != path.parent.resolve():
        raise StructuredCandidateWriteError("UNSAFE_CHUNK_DIRECTORY")
    return created


def _json_bytes(value: object) -> bytes:
    return (json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True,
    ) + "\n").encode("utf-8")
