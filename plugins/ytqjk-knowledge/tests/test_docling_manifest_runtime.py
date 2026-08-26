from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.docling_backend import DoclingBackend  # noqa: E402
from scripts.pdf_document_extractor import PdfExtractionError  # noqa: E402


def _backend(root: Path) -> DoclingBackend:
    rapid = {
        "det": "RapidOcr/onnx/det/ch_det.onnx",
        "cls": "RapidOcr/onnx/cls/ch_cls.onnx",
        "rec": "RapidOcr/onnx/rec/ch_rec.onnx",
        "keys": "RapidOcr/paddle/rec/ppocr_keys_v1.txt",
    }
    files: dict[str, str] = {}
    for relative in (*rapid.values(), "layout/model.safetensors"):
        path = root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        content = relative.encode("utf-8")
        path.write_bytes(content)
        files[relative] = hashlib.sha256(content).hexdigest()
    (root / "manifest.json").write_text(
        json.dumps({
            "schema_version": 1,
            "files": files,
            "rapidocr": {
                "det": rapid["det"],
                "cls": rapid["cls"],
                "rec": rapid["rec"],
                "rec_keys": rapid["keys"],
            },
        }),
        encoding="utf-8",
    )
    return DoclingBackend(root, files, rapid)


def test_root_manifest_is_separate_from_hashed_model_inventory(
    tmp_path: Path,
) -> None:
    backend = _backend(tmp_path)

    root, digest, rapid = backend._verify_artifacts()

    assert root == tmp_path.resolve()
    assert len(digest) == 64
    assert set(rapid) == {"det", "cls", "rec", "keys"}


def test_other_unlisted_files_remain_blocked(tmp_path: Path) -> None:
    backend = _backend(tmp_path)
    nested = tmp_path / "nested" / "manifest.json"
    nested.parent.mkdir()
    nested.write_text("{}", encoding="utf-8")

    with pytest.raises(PdfExtractionError) as captured:
        backend._verify_artifacts()

    assert captured.value.code == "NOT_CONFIGURED"
    assert "unlisted" in str(captured.value).lower()
