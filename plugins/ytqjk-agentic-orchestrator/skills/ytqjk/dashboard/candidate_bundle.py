"""Validated, rollback-safe structured candidate artifact bundles."""

from __future__ import annotations

import hashlib
import json
import re
import uuid
from contextlib import ExitStack, contextmanager
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Iterator

from candidate_file_safety import (
    CandidateFileError,
    FileSnapshot,
    atomic_replace_file,
    candidate_lifecycle_lock as _candidate_lifecycle_lock,
    read_file_snapshot,
    verify_snapshot,
    verify_snapshots,
)
from path_safety import is_reparse


_DIGEST = re.compile(r"^[0-9a-f]{64}$")
_STRUCTURED_DOCUMENT = re.compile(r"^[0-9a-f]{64}\.md$")
_MAX_ORIGINAL_BYTES = 10 * 1024 * 1024
_ORIGINAL_SUFFIXES = frozenset({
    ".bmp", ".jpeg", ".jpg", ".pdf", ".png", ".tif", ".tiff", ".webp",
})
PROTECTED_FIELDS = (
    "status", "source", "intake_id", "candidate_id", "original_path",
    "detail_path", "source_sha256",
)


class CandidateBundleError(ValueError):
    pass


@contextmanager
def candidate_lifecycle_lock(
    root: Path,
    document: Path | str,
) -> Iterator[Path]:
    stack = ExitStack()
    try:
        candidate = stack.enter_context(
            _candidate_lifecycle_lock(root, document)
        )
    except (CandidateFileError, OSError, TimeoutError, ValueError) as error:
        stack.close()
        raise CandidateBundleError("CANDIDATE_LOCK_FAILED") from error
    try:
        yield candidate
    finally:
        try:
            stack.close()
        except (CandidateFileError, OSError, ValueError) as error:
            raise CandidateBundleError("CANDIDATE_LOCK_FAILED") from error


@dataclass(frozen=True)
class StructuredCandidateBundle:
    root: Path
    document: Path
    detail: Path
    original: Path
    chunks: Path | None
    chunk_files: tuple[Path, ...]
    content: str
    fields: dict[str, str]
    detail_content: bytes
    detail_value: dict[str, object]
    snapshots: tuple[FileSnapshot, ...]


def load_structured_bundle(
    root: Path, document: Path,
) -> StructuredCandidateBundle | None:
    root = root.absolute()
    document = document.absolute()
    base = root / "personal-experience" / "candidates" \
        / "imports" / "structured"
    structured = (
        document.parent == base.absolute()
        and _STRUCTURED_DOCUMENT.fullmatch(document.name) is not None
    )
    document_snapshot = read_file_snapshot(
        root, document, _MAX_ORIGINAL_BYTES,
    )
    try:
        content = document_snapshot.content.decode("utf-8")
    except UnicodeError as error:
        raise CandidateBundleError("CANDIDATE_UNAVAILABLE") from error
    fields, _ = _front_matter(content)
    if structured and fields.get("source") != "structured-intake":
        raise CandidateBundleError("STRUCTURED_SOURCE_INVALID")
    if not structured and fields.get("source") == "structured-intake":
        raise CandidateBundleError("STRUCTURED_DOCUMENT_MISMATCH")
    if not structured:
        return None
    _safe_existing(root, document, base)
    missing = set(PROTECTED_FIELDS) - set(fields)
    if missing or fields["status"] != "CANDIDATE" or any(
        key in fields for key in ("approved_at", "approval")
    ):
        raise CandidateBundleError("STRUCTURED_IDENTITY_INVALID")
    candidate_id = fields["candidate_id"]
    digest = fields["source_sha256"]
    if not _DIGEST.fullmatch(candidate_id) or not _DIGEST.fullmatch(digest):
        raise CandidateBundleError("STRUCTURED_IDENTITY_INVALID")
    try:
        uuid.UUID(fields["intake_id"])
    except (ValueError, AttributeError) as error:
        raise CandidateBundleError("STRUCTURED_INTAKE_ID_INVALID") from error
    if document != (base / f"{candidate_id}.md").absolute():
        raise CandidateBundleError("STRUCTURED_DOCUMENT_MISMATCH")
    detail = _artifact(
        root, fields["detail_path"], base, base / f"{candidate_id}.json"
    )
    original = _original(root, fields["original_path"], base, candidate_id)
    detail_snapshot = read_file_snapshot(root, detail, 32 * 1024 * 1024)
    detail_content = detail_snapshot.content
    detail_value = _detail(detail, detail_content, candidate_id, digest)
    original_snapshot = read_file_snapshot(
        root, original, _MAX_ORIGINAL_BYTES,
    )
    original_content = original_snapshot.content
    if (
        not original_content
        or len(original_content) > _MAX_ORIGINAL_BYTES
        or hashlib.sha256(original_content).hexdigest() != digest
    ):
        raise CandidateBundleError("STRUCTURED_ORIGINAL_DIGEST_MISMATCH")
    chunks = _chunks(root, fields["intake_id"])
    chunk_files = tuple(_chunk_files(root, chunks)) if chunks else ()
    chunk_snapshots = tuple(
        read_file_snapshot(root, path, _MAX_ORIGINAL_BYTES)
        for path in chunk_files
    )
    snapshots = (
        document_snapshot, detail_snapshot, original_snapshot,
        *chunk_snapshots,
    )
    verify_snapshots(snapshots)
    return StructuredCandidateBundle(
        root, document, detail, original, chunks, chunk_files, content,
        fields, detail_content, detail_value, snapshots,
    )


def verify_structured_bundle(bundle: StructuredCandidateBundle) -> None:
    verify_snapshots(bundle.snapshots)


def validate_structured_edit(
    bundle: StructuredCandidateBundle, content: str,
) -> None:
    fields, _ = _front_matter(content)
    changed = any(
        fields.get(key) != bundle.fields[key] for key in PROTECTED_FIELDS
    )
    if changed or any(key in fields for key in ("approved_at", "approval")):
        raise CandidateBundleError("STRUCTURED_PROTECTED_FIELD_CHANGED")


def delete_bundle(bundle: StructuredCandidateBundle) -> None:
    verify_structured_bundle(bundle)
    try:
        for snapshot in bundle.snapshots:
            verify_snapshot(snapshot)
            _delete_path(snapshot.path)
        if bundle.chunks is not None:
            bundle.chunks.rmdir()
    except Exception as error:
        try:
            for snapshot in bundle.snapshots:
                if not snapshot.path.exists():
                    snapshot.path.parent.mkdir(parents=True, exist_ok=True)
                    atomic_replace_file(
                        bundle.root, snapshot.path, snapshot.content,
                    )
        except Exception as rollback_error:
            raise CandidateBundleError(
                "STRUCTURED_DELETE_ROLLBACK_FAILED"
            ) from rollback_error
        raise CandidateBundleError("STRUCTURED_DELETE_FAILED") from error


def _front_matter(content: str) -> tuple[dict[str, str], int]:
    lines = content.splitlines()
    if not lines or lines[0] != "---":
        return {}, 0
    try:
        end = lines.index("---", 1)
    except ValueError as error:
        raise CandidateBundleError("FRONT_MATTER_INVALID") from error
    fields: dict[str, str] = {}
    for line in lines[1:end]:
        key, separator, value = line.partition(":")
        key = key.strip()
        if not separator or not key or key in fields:
            raise CandidateBundleError("FRONT_MATTER_INVALID")
        fields[key] = value.strip()
    return fields, end


def _artifact(
    root: Path, value: str, parent: Path, expected: Path,
) -> Path:
    path = _relative(root, value)
    if path != expected.absolute():
        raise CandidateBundleError("STRUCTURED_ARTIFACT_MISMATCH")
    _safe_existing(root, path, parent)
    return path


def _original(root: Path, value: str, base: Path, candidate_id: str) -> Path:
    path = _relative(root, value)
    parent = base / "originals"
    suffix = path.suffix.casefold()
    if path.parent != parent.absolute() or path.stem != candidate_id \
            or suffix not in _ORIGINAL_SUFFIXES:
        raise CandidateBundleError("STRUCTURED_ORIGINAL_MISMATCH")
    _safe_existing(root, path, parent)
    return path


def _detail(
    _path: Path, content: bytes, candidate_id: str, digest: str,
) -> dict[str, object]:
    try:
        value = json.loads(content.decode("utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as error:
        raise CandidateBundleError("STRUCTURED_DETAIL_INVALID") from error
    valid = (
        isinstance(value, dict)
        and value.get("candidate_id") == candidate_id
    )
    valid = valid and value.get("source_digest") == digest
    valid = valid and value.get("state") == "CANDIDATE"
    if not valid:
        raise CandidateBundleError("STRUCTURED_DETAIL_MISMATCH")
    return value


def _chunks(root: Path, intake_id: str) -> Path | None:
    parent = root / "personal-experience" / "candidates" \
        / "imports" / "chunks"
    path = (parent / intake_id).absolute()
    if not path.exists():
        return None
    _safe_directory(root, path, parent)
    _chunk_files(root, path)
    return path


def _chunk_files(root: Path, path: Path) -> list[Path]:
    files = list(path.iterdir())
    if any(not item.is_file() for item in files):
        raise CandidateBundleError("STRUCTURED_CHUNKS_INVALID")
    for item in files:
        _safe_existing(root, item, path)
    return files


def _relative(root: Path, value: str) -> Path:
    pure = PurePosixPath(value)
    if not value or pure.is_absolute() or "\\" in value or ".." in pure.parts:
        raise CandidateBundleError("STRUCTURED_REFERENCE_INVALID")
    return (root / pure).absolute()


def _safe_existing(root: Path, path: Path, parent: Path) -> None:
    if not _within(path, parent) or not path.is_file():
        raise CandidateBundleError("UNSAFE_CANDIDATE_ARTIFACT")
    _safe_chain(root, path)


def _safe_directory(root: Path, path: Path, parent: Path) -> None:
    if not _within(path, parent) or not path.is_dir():
        raise CandidateBundleError("UNSAFE_CANDIDATE_ARTIFACT")
    _safe_chain(root, path)


def _safe_chain(root: Path, path: Path) -> None:
    root = root.absolute()
    current = path.absolute()
    if not _within(current, root):
        raise CandidateBundleError("UNSAFE_CANDIDATE_ARTIFACT")
    while True:
        if current.exists() and is_reparse(current):
            raise CandidateBundleError("UNSAFE_REPARSE_PATH")
        if current == root:
            return
        current = current.parent


def _delete_path(path: Path) -> None:
    path.unlink()


def _within(path: Path, parent: Path) -> bool:
    try:
        path.absolute().relative_to(parent.absolute())
    except ValueError:
        return False
    return True
