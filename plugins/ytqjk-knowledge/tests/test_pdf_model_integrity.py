from __future__ import annotations

import sys
from pathlib import Path

import pytest


TESTS = Path(__file__).resolve().parent
SKILL_ROOT = TESTS.parents[0] / "skills" / "ytqjk-knowledge"
sys.path[:0] = [str(TESTS), str(SKILL_ROOT)]

import scripts.docling_backend as docling_module  # noqa: E402
from scripts.docling_backend import DoclingBackend  # noqa: E402
from scripts.pdf_document_extractor import (  # noqa: E402
    PdfExtractionError,
    PdfLimits,
)
from test_pdf_document_security import (  # noqa: E402
    MODEL_PATHS,
    PDF,
    _digest,
    _model_backend,
)


def test_model_tree_rejects_digest_extra_and_escape(tmp_path: Path) -> None:
    root, hashes, _ = _model_backend(tmp_path)
    bad = dict(hashes)
    bad[MODEL_PATHS["det"]] = "0" * 64
    with pytest.raises(PdfExtractionError):
        DoclingBackend(root, bad, MODEL_PATHS)._verify_artifacts()
    (root / "unlisted.bin").write_bytes(b"unlisted")
    with pytest.raises(PdfExtractionError):
        DoclingBackend(root, hashes, MODEL_PATHS)._verify_artifacts()
    escaped = dict(hashes)
    escaped["../outside.bin"] = _digest(b"outside")
    with pytest.raises(PdfExtractionError):
        DoclingBackend(root, escaped, MODEL_PATHS)._verify_artifacts()


def test_model_tree_is_rechecked_for_each_pass(tmp_path: Path) -> None:
    root, _, backend = _model_backend(tmp_path)
    backend._verify_artifacts()
    (root / MODEL_PATHS["det"]).write_bytes(b"tampered")
    with pytest.raises(PdfExtractionError) as caught:
        backend.extract_native(PDF, PdfLimits())
    assert caught.value.code == "NOT_CONFIGURED"


def test_model_tree_rejects_linked_file(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    root, hashes, _ = _model_backend(tmp_path)
    linked = root / MODEL_PATHS["det"]
    original = docling_module._is_link
    monkeypatch.setattr(
        docling_module,
        "_is_link",
        lambda path: path == linked or original(path),
    )
    with pytest.raises(PdfExtractionError) as caught:
        DoclingBackend(root, hashes, MODEL_PATHS)._verify_artifacts()
    assert caught.value.code == "NOT_CONFIGURED"
