from __future__ import annotations

from pathlib import Path

from rag_security import contains_high_confidence_secret


MAX_CANDIDATE_BYTES = 10 * 1024 * 1024


def candidate_document(root: Path, raw_path: str) -> Path | None:
    path = (root / raw_path).resolve()
    candidates = ("personal-experience/candidates", "error-experience/candidates")
    if not any(_is_within(path, (root / item).resolve()) for item in candidates):
        return None
    return path if path.suffix == ".md" and path.is_file() else None


def update_candidate(root: Path, raw_path: str, content: str) -> dict[str, str]:
    path = candidate_document(root, raw_path)
    if path is None:
        raise ValueError("只能编辑候选资料。")
    if (
        not content.strip()
        or len(content.encode("utf-8")) > MAX_CANDIDATE_BYTES
        or "\x00" in content
        or contains_high_confidence_secret(content)
    ):
        raise ValueError("候选资料必须是非空文本。")
    path.write_text(content, encoding="utf-8")
    return {"path": path.relative_to(root).as_posix(), "state": "candidate"}


def delete_candidate(root: Path, raw_path: str) -> None:
    path = candidate_document(root, raw_path)
    if path is None:
        raise ValueError("只能删除候选资料。")
    intake_id = intake_identifier(root, path)
    original = original_path(root, path, intake_id)
    chunks = chunk_directory(root, path, intake_id)
    path.unlink()
    if original is not None and original.is_file():
        original.unlink()
    if chunks is not None and chunks.is_dir():
        for item in chunks.iterdir():
            if item.is_file():
                item.unlink()
        chunks.rmdir()


def original_path(root: Path, document: Path, intake_id: str | None) -> Path | None:
    for line in document.read_text(encoding="utf-8").splitlines():
        if not line.startswith("original_path: "):
            continue
        candidate = (root / line.removeprefix("original_path: ").strip()).resolve()
        if not _is_within(candidate, (root / "personal-experience" / "candidates" / "imports" / "originals").resolve()):
            return None
        return candidate
    if intake_id:
        originals = root / "personal-experience/candidates/imports/originals"
        matches = list(originals.glob(f"{intake_id}-*")) if originals.is_dir() else []
        return matches[0] if len(matches) == 1 else None
    return None


def chunk_directory(root: Path, document: Path, intake_id: str | None) -> Path | None:
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
    candidate = (root / "personal-experience/candidates/imports/chunks" / intake_id).resolve()
    parent = (root / "personal-experience/candidates/imports/chunks").resolve()
    return candidate if _is_within(candidate, parent) else None


def _is_within(path: Path, parent: Path) -> bool:
    try:
        path.relative_to(parent)
    except ValueError:
        return False
    return True
