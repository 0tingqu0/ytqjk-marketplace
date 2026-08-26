from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path
from types import SimpleNamespace

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

import scripts.docling_backend as docling_module  # noqa: E402
from scripts.docling_backend import DoclingBackend  # noqa: E402
from scripts.docling_payload_parser import parse_docling_payload  # noqa: E402
from scripts.intake_extraction_contracts import (  # noqa: E402
    BlockKind,
    RecognitionEvidence,
)
from scripts.pdf_document_extractor import (  # noqa: E402
    BackendDocument,
    PdfDocumentExtractor,
    PdfExtractionError,
    PICTURE_DESCRIPTION_NOT_CONFIGURED,
    PICTURE_DESCRIPTION_UNVERIFIED,
)
from scripts.structured_document_chunks import (  # noqa: E402
    build_structured_chunks,
)


PDF = b"%PDF-1.7\nfixture"
MODEL_PATHS = {
    "det": "RapidOcr/onnx/det/ch_det.onnx",
    "cls": "RapidOcr/onnx/cls/ch_cls.onnx",
    "rec": "RapidOcr/onnx/rec/ch_rec.onnx",
    "keys": "RapidOcr/paddle/rec/ppocr_keys_v1.txt",
}


def _evidence(name: str) -> RecognitionEvidence:
    return RecognitionEvidence(name, f"{name}-v1", "a" * 64)


def _payload(description: str) -> dict[str, object]:
    return {
        "pages": {"1": {"size": {"width": 100, "height": 100}}},
        "texts": [{
            "self_ref": "#/texts/0",
            "text": "searchable document text",
            "prov": [{
                "page_no": 1,
                "bbox": {"l": 1, "t": 1, "r": 90, "b": 8},
            }],
        }],
        "pictures": [{
            "self_ref": "#/pictures/0",
            "prov": [{
                "page_no": 1,
                "bbox": {"l": 10, "t": 10, "r": 40, "b": 40},
            }],
            "meta": {"description": {"text": description}},
        }],
        "body": {"children": [
            {"$ref": "#/pictures/0"},
            {"$ref": "#/texts/0"},
        ]},
    }


class FakeBackend:
    def __init__(self, page: object) -> None:
        self._page = page

    def extract_native(self, source: bytes, limits: object) -> BackendDocument:
        return BackendDocument((self._page,), _evidence("native"), 5)

    def extract_ocr(
        self,
        source: bytes,
        page_numbers: tuple[int, ...],
        limits: object,
    ) -> BackendDocument:
        raise AssertionError("unexpected OCR")


def test_pdf_picture_description_is_searchable_but_unverified() -> None:
    description = json.dumps({
        "description": "A line chart trends upward.",
        "tags": ["line chart", "trend"],
    })
    page = parse_docling_payload(
        _payload(description),
        ocr=False,
        picture_evidence=_evidence("picture"),
    )[0]
    assert page.blocks[0].kind is BlockKind.IMAGE
    image = page.blocks[0].image_classification
    assert image is not None and image.tags == ("line chart", "trend")
    result = PdfDocumentExtractor(FakeBackend(page)).extract(PDF)
    assert result.review_reasons == (PICTURE_DESCRIPTION_UNVERIFIED,)
    chunks = build_structured_chunks(result)
    assert any("trends upward" in item.text for item in chunks)
    assert any("line chart" in item.text for item in chunks)


@pytest.mark.parametrize(
    "description",
    (
        "not json",
        '{"description":"C:\\\\private","tags":["chart"]}',
        '{"description":NaN,"tags":["chart"]}',
    ),
)
def test_invalid_pdf_picture_description_is_discarded_for_review(
    description: str,
) -> None:
    page = parse_docling_payload(
        _payload(description),
        ocr=False,
        picture_evidence=_evidence("picture"),
    )[0]
    assert all(
        block.image_classification is None for block in page.blocks
    )
    result = PdfDocumentExtractor(FakeBackend(page)).extract(PDF)
    assert result.review_reasons == (
        PICTURE_DESCRIPTION_NOT_CONFIGURED,
    )


def _model_backend(root: Path) -> tuple[DoclingBackend, Path]:
    hashes = {}
    for relative in MODEL_PATHS.values():
        path = root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        content = relative.encode("utf-8")
        path.write_bytes(content)
        hashes[relative] = hashlib.sha256(content).hexdigest()
    model = root / "HuggingFaceTB--SmolVLM-256M-Instruct"
    for name in ("config.json", "model.safetensors"):
        path = model / name
        path.parent.mkdir(parents=True, exist_ok=True)
        content = name.encode("utf-8")
        path.write_bytes(content)
        relative = path.relative_to(root).as_posix()
        hashes[relative] = hashlib.sha256(content).hexdigest()
    return DoclingBackend(root, hashes, MODEL_PATHS, model), model


def test_native_docling_disables_unreliable_picture_description(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    backend, _model = _model_backend(tmp_path)
    captured: dict[str, dict[str, object]] = {}

    class Options:
        def __init__(self, **kwargs: object) -> None:
            captured["pipeline"] = kwargs

    class Picture:
        def __init__(self, **kwargs: object) -> None:
            captured["picture"] = kwargs

    class OnnxEngine:
        pass

    class LayoutOptions:
        @staticmethod
        def from_preset(
            preset: str,
            *,
            engine_options: object,
        ) -> dict[str, object]:
            return {"preset": preset, "engine": engine_options}

    modules = {
        "docling.document_converter": SimpleNamespace(
            PdfFormatOption=lambda **kwargs: kwargs,
            DocumentConverter=lambda **kwargs: kwargs,
        ),
        "docling.datamodel.base_models": SimpleNamespace(
            InputFormat=SimpleNamespace(PDF="PDF"),
            DocumentStream=object,
        ),
        "docling.datamodel.pipeline_options": SimpleNamespace(
            LayoutObjectDetectionOptions=LayoutOptions,
            PdfPipelineOptions=Options,
            PictureDescriptionVlmOptions=Picture,
            RapidOcrOptions=object,
        ),
        "docling.datamodel.object_detection_engine_options": (
            SimpleNamespace(
                OnnxRuntimeObjectDetectionEngineOptions=OnnxEngine,
            )
        ),
    }
    versions = {"docling": "2.121.0", "rapidocr": "3.9.2"}
    monkeypatch.setattr(
        docling_module.importlib.metadata,
        "version",
        lambda name: versions[name],
    )
    monkeypatch.setattr(
        docling_module.importlib,
        "import_module",
        lambda name: modules[name],
    )
    _converter, _stream, picture = backend._runtime(False)
    assert picture is False
    assert captured["pipeline"]["enable_remote_services"] is False
    layout = captured["pipeline"]["layout_options"]
    assert layout["preset"] == "layout_heron_default"
    assert type(layout["engine"]).__name__ == "OnnxEngine"
    assert "do_picture_description" not in captured["pipeline"]
    assert "generate_picture_images" not in captured["pipeline"]
    assert "picture" not in captured
