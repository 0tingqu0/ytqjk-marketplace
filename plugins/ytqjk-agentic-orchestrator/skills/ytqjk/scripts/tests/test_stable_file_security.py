from __future__ import annotations

import os
import sys
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import stable_file  # noqa: E402
from document_runtime import DocumentRuntimeError, inventory  # noqa: E402
from stable_file import StableFileError, read_stable_bytes  # noqa: E402


def test_hard_link_is_rejected_by_read_and_inventory(tmp_path: Path) -> None:
    source = tmp_path / "source.bin"
    linked = tmp_path / "linked.bin"
    source.write_bytes(b"model")
    os.link(source, linked)

    with pytest.raises(StableFileError, match="SINGLE_LINK"):
        read_stable_bytes(source, 1024)
    with pytest.raises(DocumentRuntimeError) as captured:
        inventory(tmp_path)
    assert captured.value.code == "UNSAFE_RUNTIME_PATH"


def test_parent_reparse_is_rejected(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    parent = tmp_path / "models"
    parent.mkdir()
    artifact = parent / "model.bin"
    artifact.write_bytes(b"model")
    original = stable_file.is_reparse
    monkeypatch.setattr(
        stable_file,
        "is_reparse",
        lambda path: path == parent or original(path),
    )
    with pytest.raises(StableFileError, match="REPARSE"):
        read_stable_bytes(artifact, 1024)


def test_replacement_during_read_is_rejected(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    artifact = tmp_path / "model.bin"
    replacement = tmp_path / "replacement.bin"
    artifact.write_bytes(b"old")
    replacement.write_bytes(b"new")
    original = stable_file._read_regular

    def replace_during_read(*args: object) -> object:
        result = original(*args)
        os.replace(replacement, artifact)
        return result

    monkeypatch.setattr(stable_file, "_read_regular", replace_during_read)
    with pytest.raises(StableFileError, match="CHANGED"):
        read_stable_bytes(artifact, 1024)
