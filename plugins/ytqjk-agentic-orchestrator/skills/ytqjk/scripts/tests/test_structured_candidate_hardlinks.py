from __future__ import annotations

import os
import sys
from pathlib import Path

import pytest


TESTS = Path(__file__).resolve().parent
sys.path.insert(0, str(TESTS))

from test_structured_candidate_lifecycle import (  # noqa: E402
    SOURCE,
    _create_bundle,
    _relative,
)

import approval_promotion  # noqa: E402
import candidate_actions  # noqa: E402
import candidate_file_safety  # noqa: E402
import file_lock  # noqa: E402
from candidate_bundle import candidate_lifecycle_lock  # noqa: E402


def _assert_no_approved(root: Path) -> None:
    approved = root / "personal-experience" / "approved"
    assert not approved.exists() or not list(approved.rglob("*.*"))


@pytest.mark.parametrize(
    "artifact", ["document", "detail", "original", "chunk"],
)
def test_hardlinked_structured_artifact_blocks_approval(
    tmp_path: Path, artifact: str,
) -> None:
    bundle = _create_bundle(tmp_path)
    path = bundle[artifact]
    document = bundle["document"]
    assert isinstance(path, Path) and isinstance(document, Path)
    outside = tmp_path / f"outside-{artifact}"
    os.link(path, outside)
    before = outside.read_bytes()

    assert not approval_promotion.promote(
        tmp_path, document, require_ready=False,
    )
    assert outside.read_bytes() == before
    assert path.exists() and document.exists()
    _assert_no_approved(tmp_path)


@pytest.mark.parametrize("action", ["approve", "delete", "edit"])
def test_hardlinked_candidate_blocks_every_lifecycle_write(
    tmp_path: Path, action: str,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    content = bundle["content"]
    assert isinstance(document, Path) and isinstance(content, str)
    outside = tmp_path / "outside-candidate.md"
    os.link(document, outside)
    before = outside.read_bytes()
    raw = _relative(tmp_path, document)

    if action == "approve":
        assert not approval_promotion.promote(
            tmp_path, document, require_ready=False,
        )
    else:
        with pytest.raises(ValueError, match="NOT_SINGLE_LINK"):
            if action == "delete":
                candidate_actions.delete_candidate(tmp_path, raw)
            else:
                candidate_actions.update_candidate(
                    tmp_path, raw, content.replace("Body", "Edited"),
                )
    assert outside.read_bytes() == before
    assert document.read_bytes() == before
    _assert_no_approved(tmp_path)


def test_hardlinked_lock_is_rejected_before_external_write(
    tmp_path: Path,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    with candidate_lifecycle_lock(tmp_path, document):
        pass
    lock = next((tmp_path / ".locks").glob("candidate-*.lock"))
    lock.unlink()
    outside = tmp_path / "outside-lock"
    outside.write_bytes(b"")
    os.link(outside, lock)

    with pytest.raises(ValueError, match="CANDIDATE_LOCK_FAILED"):
        with candidate_lifecycle_lock(tmp_path, document):
            raise AssertionError("hardlinked lock unexpectedly acquired")
    assert outside.read_bytes() == b""
    assert lock.stat().st_nlink == 2


def test_lock_hardlink_added_after_open_blocks_before_write(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    outside = tmp_path / "outside-late-lock"
    real_validate = file_lock._validate_open_file

    def race(path: Path, handle: object) -> object:
        result = real_validate(path, handle)
        if not outside.exists():
            os.link(path, outside)
        return result

    monkeypatch.setattr(file_lock, "_validate_open_file", race)
    with pytest.raises(ValueError, match="CANDIDATE_LOCK_FAILED"):
        with candidate_lifecycle_lock(tmp_path, document):
            raise AssertionError("late hardlink unexpectedly acquired")
    assert outside.read_bytes() == b""


def test_open_handle_path_swap_is_detected(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    replacement = tmp_path / "replacement.md"
    replacement.write_bytes(b"replacement")
    real_fstat = candidate_file_safety.os.fstat
    calls = 0

    def race(descriptor: int) -> os.stat_result:
        nonlocal calls
        result = real_fstat(descriptor)
        calls += 1
        if calls == 2:
            os.replace(replacement, document)
        return result

    monkeypatch.setattr(candidate_file_safety.os, "fstat", race)
    with pytest.raises(
        ValueError,
        match="CANDIDATE_FILE_(?:CHANGED|UNAVAILABLE)",
    ):
        candidate_file_safety.read_file_snapshot(
            tmp_path, document, 10 * 1024 * 1024,
        )


def test_hardlink_added_after_scan_blocks_before_commit(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    original = bundle["original"]
    assert isinstance(document, Path) and isinstance(original, Path)
    outside = tmp_path / "outside-race.png"
    real_scan = approval_promotion._structured_scan_clean

    def race(candidate: object) -> bool:
        result = real_scan(candidate)
        os.link(original, outside)
        return result

    monkeypatch.setattr(
        approval_promotion, "_structured_scan_clean", race,
    )
    assert not approval_promotion.promote(
        tmp_path, document, require_ready=False,
    )
    assert document.exists() and original.exists() and outside.exists()
    _assert_no_approved(tmp_path)


def test_hardlink_added_after_edit_validation_blocks_commit(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    content = bundle["content"]
    assert isinstance(document, Path) and isinstance(content, str)
    outside = tmp_path / "outside-edit.md"
    real_validate = candidate_actions.validate_structured_edit

    def race(candidate: object, proposed: str) -> None:
        real_validate(candidate, proposed)
        os.link(document, outside)

    monkeypatch.setattr(
        candidate_actions, "validate_structured_edit", race,
    )
    with pytest.raises(ValueError, match="NOT_SINGLE_LINK"):
        candidate_actions.update_candidate(
            tmp_path,
            _relative(tmp_path, document),
            content.replace("Body", "Edited"),
        )
    assert document.read_text(encoding="utf-8") == content
    assert outside.read_text(encoding="utf-8") == content


def test_hardlinked_approved_target_does_not_modify_external(
    tmp_path: Path,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    original = bundle["original"]
    assert isinstance(document, Path) and isinstance(original, Path)
    target = Path(str(original).replace("candidates", "approved", 1))
    target.parent.mkdir(parents=True)
    outside = tmp_path / "outside-approved-target"
    outside.write_bytes(b"external")
    os.link(outside, target)

    assert not approval_promotion.promote(
        tmp_path, document, require_ready=False,
    )
    assert outside.read_bytes() == b"external"
    assert target.read_bytes() == b"external"
    assert document.exists() and original.exists()


def test_approved_bundle_uses_new_single_link_inodes(tmp_path: Path) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    original = bundle["original"]
    assert isinstance(document, Path) and isinstance(original, Path)
    sources = []
    for key in ("document", "detail", "original", "chunk"):
        source = bundle[key]
        assert isinstance(source, Path)
        sources.append((source, source.stat().st_ino))
    outside = tmp_path / "outside-copy.png"
    outside.write_bytes(SOURCE)

    assert approval_promotion.promote(
        tmp_path, document, require_ready=False,
    )
    for source, source_inode in sources:
        approved = Path(str(source).replace("candidates", "approved", 1))
        assert approved.stat().st_ino != source_inode
        assert approved.stat().st_nlink == 1
    outside.write_bytes(b"changed outside")
    approved_original = Path(
        str(original).replace("candidates", "approved", 1)
    )
    assert approved_original.read_bytes() == SOURCE
