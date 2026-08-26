from __future__ import annotations

import hashlib
from pathlib import Path

from candidate_bundle import (
    candidate_lifecycle_lock,
    delete_bundle,
    load_structured_bundle,
    validate_structured_edit,
    verify_structured_bundle,
)
from candidate_file_safety import (
    atomic_replace_file,
    read_file_snapshot,
    verify_snapshot,
)
from path_safety import is_reparse
from rag_security import contains_high_confidence_secret


MAX_CANDIDATE_BYTES = 10 * 1024 * 1024
INTERNAL_CANDIDATE_DIRECTORIES = frozenset({"chunks", "originals"})


def candidate_document(root: Path, raw_path: str) -> Path | None:
    root = root.absolute()
    path = (root / raw_path).absolute()
    candidates = (
        root / "personal-experience" / "candidates",
        root / "error-experience" / "candidates",
    )
    parent = next(
        (item for item in candidates if _is_within(path, item)), None
    )
    if parent is None:
        return None
    relative = path.relative_to(parent)
    if INTERNAL_CANDIDATE_DIRECTORIES.intersection(relative.parts[:-1]):
        return None
    if path.suffix != ".md" or not path.is_file():
        return None
    current = path
    while True:
        if current.exists() and is_reparse(current):
            return None
        if current == root:
            return path
        current = current.parent



def candidate_version(content: str | bytes) -> str:
    value = content.encode("utf-8") if isinstance(content, str) else content
    normalized = value.replace(b"\r\n", b"\n").replace(b"\r", b"\n")
    return hashlib.sha256(normalized).hexdigest()


def update_candidate(
    root: Path,
    raw_path: str,
    content: str,
    expected_version: str | None = None,
) -> dict[str, str]:
    with candidate_lifecycle_lock(root, raw_path) as candidate:
        if (
            not content.strip()
            or len(content.encode("utf-8")) > MAX_CANDIDATE_BYTES
            or "\x00" in content
            or contains_high_confidence_secret(content)
        ):
            raise ValueError("候选资料必须是非空文本。")
        canonical_root = root.resolve(strict=True)
        path = candidate_document(canonical_root, str(candidate))
        if path is None:
            raise ValueError("只能编辑候选资料。")
        bundle = load_structured_bundle(canonical_root, path)
        if bundle is not None:
            validate_structured_edit(bundle, content)
            verify_structured_bundle(bundle)
            expected = bundle.snapshots[0]
        else:
            expected = read_file_snapshot(
                canonical_root, path, MAX_CANDIDATE_BYTES,
            )
        if (
            expected_version is not None
            and candidate_version(expected.content) != expected_version
        ):
            raise ValueError("CANDIDATE_VERSION_CONFLICT")
        written = atomic_replace_file(
            canonical_root, path, content.encode("utf-8"), expected,
        )
        return {
            "path": path.relative_to(canonical_root).as_posix(),
            "state": "candidate",
            "version": candidate_version(written.content),
        }


def delete_candidate(root: Path, raw_path: str) -> None:
    with candidate_lifecycle_lock(root, raw_path) as candidate:
        canonical_root = root.resolve(strict=True)
        path = candidate_document(canonical_root, str(candidate))
        if path is None:
            raise ValueError("只能删除候选资料。")
        bundle = load_structured_bundle(canonical_root, path)
        if bundle is not None:
            delete_bundle(bundle)
            return
        candidate_snapshot = read_file_snapshot(
            canonical_root, path, MAX_CANDIDATE_BYTES,
        )
        intake_id = intake_identifier(canonical_root, path)
        original = original_path(canonical_root, path, intake_id)
        chunks = chunk_directory(canonical_root, path, intake_id)
        verify_snapshot(candidate_snapshot)
        path.unlink()
        if original is not None and original.is_file():
            original.unlink()
        if chunks is not None and chunks.is_dir():
            for item in chunks.iterdir():
                if item.is_file():
                    item.unlink()
            chunks.rmdir()


def original_path(
    root: Path, document: Path, intake_id: str | None,
) -> Path | None:
    for line in document.read_text(encoding="utf-8").splitlines():
        if not line.startswith("original_path: "):
            continue
        value = line.removeprefix("original_path: ").strip()
        candidate = (root / value).resolve()
        parent = root / "personal-experience" / "candidates" \
            / "imports" / "originals"
        if not _is_within(candidate, parent.resolve()):
            return None
        return candidate
    if intake_id:
        originals = root / "personal-experience/candidates/imports/originals"
        matches = (
            list(originals.glob(f"{intake_id}-*"))
            if originals.is_dir() else []
        )
        return matches[0] if len(matches) == 1 else None
    return None


def chunk_directory(
    root: Path, document: Path, intake_id: str | None,
) -> Path | None:
    for line in document.read_text(encoding="utf-8").splitlines():
        if not line.startswith("intake_id: "):
            continue
        intake_id = line.removeprefix("intake_id: ").strip()
        return _chunk_path(root, intake_id)
    return _chunk_path(root, intake_id) if intake_id else None


def intake_identifier(root: Path, document: Path) -> str | None:
    for line in document.read_text(encoding="utf-8").splitlines():
        if line.startswith("intake_id: "):
            return line.removeprefix("intake_id: ").strip()
    imports = (root / "personal-experience/candidates/imports").resolve()
    return document.stem if _is_within(document, imports) else None


def _chunk_path(root: Path, intake_id: str) -> Path | None:
    if not intake_id or "/" in intake_id or "\\" in intake_id:
        return None
    candidate = (
        root / "personal-experience/candidates/imports/chunks" / intake_id
    ).resolve()
    parent = (root / "personal-experience/candidates/imports/chunks").resolve()
    return candidate if _is_within(candidate, parent) else None


def _is_within(path: Path, parent: Path) -> bool:
    try:
        path.relative_to(parent)
    except ValueError:
        return False
    return True
