from __future__ import annotations

import hashlib
import json
import re
from datetime import datetime, timezone
from pathlib import Path

from approval_assessment import assess_for_approval
from candidate_bundle import (
    CandidateBundleError,
    StructuredCandidateBundle,
    candidate_lifecycle_lock,
    load_structured_bundle,
    verify_structured_bundle,
)
from candidate_actions import candidate_document
from candidate_file_safety import (
    FileSnapshot,
    atomic_replace_file,
    read_file_snapshot,
    verify_snapshot,
)
from knowledge_engine_locator import locate_knowledge_engine
from path_safety import is_reparse
from rag_security import contains_high_confidence_secret
from structured_candidate_writer import (
    StructuredCandidateWriteError,
    promote_chunk_bytes,
)


def promote(root: Path, path: Path, *, require_ready: bool = True) -> bool:
    try:
        canonical_root = root.resolve(strict=True)
        document = candidate_document(canonical_root, str(path))
        if document is None:
            return False
        with candidate_lifecycle_lock(root, document) as candidate:
            return _promote_locked(
                canonical_root, candidate, require_ready,
            )
    except (
        CandidateBundleError,
        OSError,
        StructuredCandidateWriteError,
        UnicodeError,
        ValueError,
    ):
        return False


def _promote_locked(root: Path, path: Path, require_ready: bool) -> bool:
    try:
        bundle = load_structured_bundle(root, path)
        if bundle is None:
            source = read_file_snapshot(root, path, 10 * 1024 * 1024)
            content = source.content.decode("utf-8").replace(
                "\r\n", "\n",
            ).replace("\r", "\n")
        else:
            source = bundle.snapshots[0]
            content = bundle.content
    except (CandidateBundleError, OSError, UnicodeError, ValueError):
        return False
    if contains_high_confidence_secret(content):
        return False
    if bundle is not None and not _structured_scan_clean(bundle):
        return False
    assessment = assess_for_approval(assessment_content(content), False)
    if require_ready and assessment["decision"] != "READY_FOR_REVIEW":
        return False
    approval = "reviewed-evidence-gate" if require_ready else "manual-dashboard"
    approved_at = datetime.now(timezone.utc).isoformat()
    if bundle is not None:
        approved = approved_content(
            bundle, approved_at, approval
        )
        try:
            promote_bundle(bundle, approved, approved_at, approval)
        except CandidateBundleError:
            return False
        return True
    relative = path.relative_to(root).as_posix()
    target = root / relative.replace("/candidates/", "/approved/", 1)
    target.parent.mkdir(parents=True, exist_ok=True)
    approved = re.sub(
        r"(?m)^status: CANDIDATE$", "status: APPROVED", content, count=1
    )
    if approved == content:
        approved = (
            f"---\nstatus: APPROVED\napproved_at: {approved_at}\n"
            f"---\n\n{content}"
        )
    else:
        metadata = f"\napproved_at: {approved_at}\napproval: {approval}\n---"
        approved = approved.replace("\n---", metadata, 1)
    _commit_write(target, approved.encode("utf-8"))
    verify_snapshot(source)
    _delete_path(path)
    return True


def _structured_scan_clean(bundle: StructuredCandidateBundle) -> bool:
    try:
        verify_structured_bundle(bundle)
        scanner = _local_scanner()
        if not scanner.ready():
            return False
        document, detail, original, *chunks = bundle.snapshots
        original_content = original.content
        if hashlib.sha256(original_content).hexdigest() != bundle.fields[
            "source_sha256"
        ]:
            return False
        artifacts = [
            ("candidate-document", document.content),
            ("candidate-detail-raw", detail.content),
            ("candidate-detail-canonical", _json_bytes(bundle.detail_value)),
            ("candidate-original-binary", original_content),
        ]
        for index, snapshot in enumerate(chunks):
            content = snapshot.content
            content.decode("utf-8")
            artifacts.append((f"candidate-chunk-{index}", content))
        for phase, content in artifacts:
            result = scanner.scan(content, phase)
            if (
                getattr(result.state, "value", None) != "CLEAN"
                or result.sha256 != hashlib.sha256(content).hexdigest()
                or result.size_bytes != len(content)
            ):
                return False
        verify_structured_bundle(bundle)
        return True
    except Exception:
        return False


def _local_scanner() -> object:
    plugin = Path(__file__).resolve().parents[3]
    engine = locate_knowledge_engine(plugin)
    return engine.module("intake_security").LocalScanner()


def assessment_content(content: str) -> str:
    marker = "## 原始资料\n\n"
    return content.partition(marker)[2] if marker in content else content


def approved_content(
    bundle: StructuredCandidateBundle,
    approved_at: str,
    approval: str,
) -> str:
    detail = _approved_path(bundle.root, bundle.detail)
    original = _approved_path(bundle.root, bundle.original)
    updates = {
        "status": "APPROVED",
        "detail_path": detail.relative_to(bundle.root).as_posix(),
        "original_path": original.relative_to(bundle.root).as_posix(),
    }
    lines = bundle.content.splitlines()
    end = lines.index("---", 1)
    for index in range(1, end):
        key = lines[index].partition(":")[0].strip()
        if key in updates:
            lines[index] = f"{key}: {updates[key]}"
    lines[end:end] = [f"approved_at: {approved_at}", f"approval: {approval}"]
    suffix = "\n" if bundle.content.endswith("\n") else ""
    return "\n".join(lines) + suffix


def promote_bundle(
    bundle: StructuredCandidateBundle, content: str,
    approved_at: str, approval: str,
) -> Path:
    detail = dict(bundle.detail_value)
    detail.update({
        "state": "APPROVED", "approved_at": approved_at,
        "approval": approval,
    })
    document, detail_source, original, *chunks = bundle.snapshots
    target_document = _approved_path(bundle.root, document.path)
    target_detail = _approved_path(bundle.root, detail_source.path)
    targets = [
        (
            original,
            _approved_path(bundle.root, original.path),
            original.content,
        ),
        *(
            (
                item,
                _approved_path(bundle.root, item.path),
                promote_chunk_bytes(item.content, approved_at, approval),
            )
            for item in chunks
        ),
        (detail_source, target_detail, _json_bytes(detail)),
        (document, target_document, content.encode("utf-8")),
    ]
    created: list[FileSnapshot] = []
    created_dirs: list[Path] = []
    try:
        for _source, target, _content in targets:
            created_dirs.extend(_prepare_target(bundle.root, target))
        verify_structured_bundle(bundle)
        for source, target, target_content in targets:
            verify_snapshot(source)
            _commit_write(target, target_content)
            created.append(read_file_snapshot(
                bundle.root, target, max(len(target_content), 1),
            ))
        verify_structured_bundle(bundle)
        sources = (*bundle.snapshots[1:], bundle.snapshots[0])
        for source in sources:
            verify_snapshot(source)
            _delete_path(source.path)
        if bundle.chunks is not None:
            bundle.chunks.rmdir()
    except Exception as error:
        _rollback(bundle, created, created_dirs)
        raise CandidateBundleError("STRUCTURED_PROMOTION_FAILED") from error
    return target_document


def _approved_path(root: Path, source: Path) -> Path:
    relative = source.relative_to(root).as_posix()
    if "/candidates/" not in relative:
        raise CandidateBundleError("STRUCTURED_CANDIDATE_SCOPE_INVALID")
    target = root / relative.replace("/candidates/", "/approved/", 1)
    approved = root / "personal-experience" / "approved"
    if not _within(target, approved):
        raise CandidateBundleError("STRUCTURED_APPROVED_SCOPE_INVALID")
    return target.absolute()


def _prepare_target(root: Path, path: Path) -> tuple[Path, ...]:
    approved = root / "personal-experience" / "approved"
    if not _within(path, approved):
        raise CandidateBundleError("UNSAFE_APPROVED_ARTIFACT")
    _safe_chain(root, path.parent)
    if path.exists() or is_reparse(path):
        raise CandidateBundleError("APPROVED_ARTIFACT_EXISTS")
    missing = []
    current = path.parent
    while not current.exists():
        missing.append(current)
        current = current.parent
    path.parent.mkdir(parents=True, exist_ok=True)
    _safe_chain(root, path.parent)
    return tuple(missing)


def _safe_chain(root: Path, path: Path) -> None:
    current = path.absolute()
    root = root.absolute()
    if not _within(current, root):
        raise CandidateBundleError("UNSAFE_APPROVED_ARTIFACT")
    while True:
        if current.exists() and is_reparse(current):
            raise CandidateBundleError("UNSAFE_REPARSE_PATH")
        if current == root:
            return
        current = current.parent


def _rollback(
    bundle: StructuredCandidateBundle,
    created: list[FileSnapshot],
    created_dirs: list[Path],
) -> None:
    for target in reversed(created):
        target.path.unlink(missing_ok=True)
    for source in bundle.snapshots:
        if not source.path.exists():
            source.path.parent.mkdir(parents=True, exist_ok=True)
            atomic_replace_file(
                bundle.root, source.path, source.content,
            )
    for directory in created_dirs:
        try:
            directory.rmdir()
        except OSError:
            pass


def _json_bytes(value: object) -> bytes:
    text = json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True,
    )
    return (text + "\n").encode("utf-8")


def _commit_write(path: Path, content: bytes) -> None:
    atomic_replace_file(path.parent, path, content)


def _delete_path(path: Path) -> None:
    path.unlink()


def _within(path: Path, parent: Path) -> bool:
    try:
        path.absolute().relative_to(parent.absolute())
    except ValueError:
        return False
    return True
