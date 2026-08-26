from __future__ import annotations

import hashlib
import json
import sys
import uuid
from pathlib import Path

import pytest


DASHBOARD = Path(__file__).resolve().parents[2] / "dashboard"
SCRIPTS = DASHBOARD.parent / "scripts"
sys.path.insert(0, str(SCRIPTS))
sys.path.insert(0, str(DASHBOARD))

import candidate_bundle  # noqa: E402
import approval_promotion  # noqa: E402
from approval_promotion import promote  # noqa: E402
from candidate_actions import (  # noqa: E402
    candidate_document,
    delete_candidate,
    update_candidate,
)
from dashboard_documents import snapshot  # noqa: E402


CANDIDATE_ID = "a" * 64
SOURCE = b"structured-source"
DIGEST = hashlib.sha256(SOURCE).hexdigest()


def _create_bundle(root: Path) -> dict[str, Path | str | bytes]:
    base = root / "personal-experience" / "candidates" \
        / "imports" / "structured"
    intake_id = str(uuid.uuid4())
    document = base / f"{CANDIDATE_ID}.md"
    detail = base / f"{CANDIDATE_ID}.json"
    original = base / "originals" / f"{CANDIDATE_ID}.png"
    chunks = root / "personal-experience" / "candidates" \
        / "imports" / "chunks" / intake_id
    original.parent.mkdir(parents=True)
    chunks.mkdir(parents=True)
    original.write_bytes(SOURCE)
    chunk = chunks / "chunk-1.md"
    chunk.write_text(
        "---\nstatus: CANDIDATE\n"
        "source: structured-intake-chunk\n"
        f"intake_id: {intake_id}\ncandidate_id: {CANDIDATE_ID}\n"
        "chunk_number: 1\n---\n\n# Chunk\n\nchunk\n",
        encoding="utf-8",
    )
    detail_value = {
        "candidate_id": CANDIDATE_ID,
        "source_digest": DIGEST,
        "state": "CANDIDATE",
        "metadata": {"title": "Diagram"},
    }
    detail.write_text(
        json.dumps(detail_value, sort_keys=True), encoding="utf-8"
    )
    detail_ref = detail.relative_to(root).as_posix()
    original_ref = original.relative_to(root).as_posix()
    content = (
        "---\nstatus: CANDIDATE\nsource: structured-intake\n"
        f"intake_id: {intake_id}\ncandidate_id: {CANDIDATE_ID}\n"
        f"original_path: {original_ref}\ndetail_path: {detail_ref}\n"
        f"source_sha256: {DIGEST}\n---\n\n# Diagram\n\nBody\n"
    )
    document.write_text(content, encoding="utf-8")
    return {
        "document": document, "detail": detail, "original": original,
        "chunks": chunks, "chunk": chunk, "content": content,
    }


def _relative(root: Path, path: Path) -> str:
    return path.relative_to(root).as_posix()


def test_structured_bundle_exposes_one_logical_candidate(
    tmp_path: Path,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)

    data = snapshot(tmp_path)
    candidates = [
        item for item in data["documents"]
        if item["state"] == "candidate"
    ]

    assert data["counts"]["candidate"] == 1
    assert [item["path"] for item in candidates] == [
        _relative(tmp_path, document)
    ]


@pytest.mark.parametrize("artifact_name", ("chunk", "original"))
def test_internal_bundle_artifact_actions_fail_closed(
    tmp_path: Path,
    artifact_name: str,
) -> None:
    bundle = _create_bundle(tmp_path)
    artifact = bundle[artifact_name]
    assert isinstance(artifact, Path)
    raw_path = _relative(tmp_path, artifact)

    assert candidate_document(tmp_path, raw_path) is None
    assert not promote(tmp_path, artifact, require_ready=False)
    with pytest.raises(ValueError):
        update_candidate(tmp_path, raw_path, "forbidden")
    with pytest.raises(ValueError):
        delete_candidate(tmp_path, raw_path)
    assert artifact.exists()


def test_delete_removes_complete_structured_bundle(tmp_path: Path) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)

    delete_candidate(tmp_path, _relative(tmp_path, document))

    for key in ("document", "detail", "original", "chunks"):
        path = bundle[key]
        assert isinstance(path, Path)
        assert not path.exists()


def test_promote_moves_bundle_and_updates_references(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)

    assessment = approval_promotion.assess_for_approval
    monkeypatch.setattr(
        approval_promotion, "assess_for_approval",
        lambda *_args: {"decision": "NOT_READY"},
    )
    assert not promote(tmp_path, document, require_ready=True)
    assert document.is_file()
    monkeypatch.setattr(approval_promotion, "assess_for_approval", assessment)
    assert promote(tmp_path, document, require_ready=False)

    approved = Path(str(document).replace("candidates", "approved", 1))
    approved_detail = approved.with_suffix(".json")
    approved_original = approved.parent / "originals" \
        / f"{CANDIDATE_ID}.png"
    chunks = bundle["chunks"]
    assert isinstance(chunks, Path)
    approved_chunks = Path(str(chunks).replace("candidates", "approved", 1))
    content = approved.read_text(encoding="utf-8")
    assert "status: APPROVED" in content
    assert _relative(tmp_path, approved_detail) in content
    assert _relative(tmp_path, approved_original) in content
    detail = json.loads(approved_detail.read_text(encoding="utf-8"))
    assert detail["state"] == "APPROVED"
    assert detail["approval"] == "manual-dashboard"
    assert approved_original.read_bytes() == SOURCE
    approved_chunk = approved_chunks / "chunk-1.md"
    assert approved_chunk.is_file()
    assert "status: APPROVED" in approved_chunk.read_text(encoding="utf-8")
    for key in ("document", "detail", "original", "chunks"):
        path = bundle[key]
        assert isinstance(path, Path)
        assert not path.exists()


def test_edit_preserves_protected_identity_and_allows_body(
    tmp_path: Path,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    content = bundle["content"]
    assert isinstance(document, Path)
    assert isinstance(content, str)
    raw_path = _relative(tmp_path, document)

    update_candidate(tmp_path, raw_path, content.replace("Body", "New body"))
    changed = document.read_text(encoding="utf-8")
    assert "New body" in changed
    with pytest.raises(ValueError, match="PROTECTED_FIELD"):
        update_candidate(
            tmp_path, raw_path,
            changed.replace(CANDIDATE_ID, "b" * 64, 1),
        )
    assert document.read_text(encoding="utf-8") == changed


def test_cross_area_reference_and_digest_tamper_fail_closed(
    tmp_path: Path,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    detail = bundle["detail"]
    original = bundle["original"]
    assert isinstance(document, Path)
    assert isinstance(detail, Path)
    assert isinstance(original, Path)
    original.write_bytes(b"tampered")

    assert not promote(tmp_path, document, require_ready=False)
    assert document.exists()
    original.write_bytes(SOURCE)
    detail_bytes = detail.read_bytes()
    detail_value = json.loads(detail_bytes)
    detail_value["candidate_id"] = "b" * 64
    detail.write_text(json.dumps(detail_value), encoding="utf-8")
    assert not promote(tmp_path, document, require_ready=False)
    detail.write_bytes(detail_bytes)
    content = document.read_text(encoding="utf-8")
    content = content.replace(
        "personal-experience/candidates/imports/structured/originals/",
        "personal-experience/approved/imports/structured/originals/",
    )
    document.write_text(content, encoding="utf-8")
    assert not promote(tmp_path, document, require_ready=False)
    with pytest.raises(ValueError, match="REFERENCE|ORIGINAL"):
        delete_candidate(tmp_path, _relative(tmp_path, document))
    assert document.exists()
    assert detail.exists()
    assert original.exists()


def test_promotion_mid_failure_rolls_back_all_artifacts(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    calls = 0
    original_write = approval_promotion._commit_write

    def fail_second(path: Path, content: bytes) -> None:
        nonlocal calls
        calls += 1
        if calls == 2:
            raise OSError("injected promotion failure")
        original_write(path, content)

    monkeypatch.setattr(approval_promotion, "_commit_write", fail_second)
    assert not promote(tmp_path, document, require_ready=False)
    for key in ("document", "detail", "original", "chunk"):
        path = bundle[key]
        assert isinstance(path, Path)
        assert path.exists()
    approved = tmp_path / "personal-experience" / "approved"
    assert not list(approved.rglob("*.*"))


def test_promotion_delete_failure_restores_exact_detail(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    detail = bundle["detail"]
    assert isinstance(document, Path)
    assert isinstance(detail, Path)
    detail_bytes = detail.read_bytes()
    calls = 0
    original_delete = approval_promotion._delete_path

    def fail_second(path: Path) -> None:
        nonlocal calls
        calls += 1
        if calls == 2:
            raise OSError("injected source delete failure")
        original_delete(path)

    monkeypatch.setattr(approval_promotion, "_delete_path", fail_second)
    assert not promote(tmp_path, document, require_ready=False)
    assert detail.read_bytes() == detail_bytes
    for key in ("document", "detail", "original", "chunk"):
        path = bundle[key]
        assert isinstance(path, Path)
        assert path.exists()


def test_delete_mid_failure_restores_all_artifacts(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    calls = 0
    original_delete = candidate_bundle._delete_path

    def fail_second(path: Path) -> None:
        nonlocal calls
        calls += 1
        if calls == 2:
            raise OSError("injected delete failure")
        original_delete(path)

    monkeypatch.setattr(candidate_bundle, "_delete_path", fail_second)
    with pytest.raises(ValueError, match="STRUCTURED_DELETE_FAILED"):
        delete_candidate(tmp_path, _relative(tmp_path, document))
    for key in ("document", "detail", "original", "chunk"):
        path = bundle[key]
        assert isinstance(path, Path)
        assert path.exists()


def test_reparse_artifact_is_rejected(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    original = bundle["original"]
    assert isinstance(document, Path)
    assert isinstance(original, Path)
    real_check = candidate_bundle.is_reparse
    monkeypatch.setattr(
        candidate_bundle, "is_reparse",
        lambda path: path == original or real_check(path),
    )

    assert not promote(tmp_path, document, require_ready=False)
    assert document.exists()
    assert original.exists()
