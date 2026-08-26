from __future__ import annotations

import hashlib
import importlib.metadata
import sys
from collections.abc import Mapping
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
    BoundingBox,
    CoordinateUnit,
    ExtractionMode,
    QualityStatus,
    RecognitionEvidence,
)
from scripts.pdf_document_extractor import (  # noqa: E402
    BackendBlock,
    BackendDocument,
    BackendPage,
    PdfDocumentExtractor,
    PdfExtractionError,
    PdfLimits,
)


PDF = b"%PDF-1.7\nfixture"
MODEL_PATHS = {
    "det": "rapid/det.onnx",
    "cls": "rapid/cls.onnx",
    "rec": "rapid/rec.onnx",
    "keys": "rapid/keys.txt",
}


def _digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def _model_backend(path: Path) -> tuple[Path, dict[str, str], DoclingBackend]:
    root = path / "models"
    hashes: dict[str, str] = {}
    for key, relative in MODEL_PATHS.items():
        content = f"fixed-{key}".encode()
        path = root / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        hashes[relative] = _digest(content)
    return root, hashes, DoclingBackend(root, hashes, MODEL_PATHS)


def _tree_digest(hashes: Mapping[str, str]) -> str:
    tree = hashlib.sha256()
    for relative, digest in sorted(hashes.items()):
        tree.update(relative.encode())
        tree.update(b"\0")
        tree.update(digest.encode())
        tree.update(b"\n")
    return tree.hexdigest()


def _fake_runtime(monkeypatch: pytest.MonkeyPatch) -> dict[str, object]:
    captured: dict[str, object] = {}

    class OnnxEngine:
        pass

    class LayoutOptions:
        @staticmethod
        def from_preset(
            preset: str, *, engine_options: object,
        ) -> dict[str, object]:
            return {"preset": preset, "engine": engine_options}

    class RapidOptions:
        def __init__(self, **kwargs: object) -> None:
            captured["rapid"] = kwargs

    class PipelineOptions:
        def __init__(self, **kwargs: object) -> None:
            captured["pipeline"] = kwargs

    class Converter:
        def __init__(self, **kwargs: object) -> None:
            captured["converter"] = kwargs

    converter = SimpleNamespace(
        PdfFormatOption=lambda **kwargs: kwargs,
        DocumentConverter=Converter,
    )
    models = SimpleNamespace(
        InputFormat=SimpleNamespace(PDF="PDF"),
        DocumentStream=object,
    )
    options = SimpleNamespace(
        LayoutObjectDetectionOptions=LayoutOptions,
        PdfPipelineOptions=PipelineOptions,
        RapidOcrOptions=RapidOptions,
    )
    modules = {
        "docling.document_converter": converter,
        "docling.datamodel.base_models": models,
        "docling.datamodel.pipeline_options": options,
        "docling.datamodel.object_detection_engine_options": (
            SimpleNamespace(
                OnnxRuntimeObjectDetectionEngineOptions=OnnxEngine,
            )
        ),
    }
    versions = {"docling": "2.121.0", "rapidocr": "3.9.2"}
    monkeypatch.setattr(
        docling_module.importlib.metadata, "version",
        lambda name: versions[name],
    )
    monkeypatch.setattr(
        docling_module.importlib, "import_module",
        lambda name: modules[name],
    )
    return captured


def _box(x: float = 10, y: float = 10) -> BoundingBox:
    return BoundingBox(x, y, 40, 10, CoordinateUnit.POINTS)


def _block(text: str, confidence: float, x: float = 10) -> BackendBlock:
    return BackendBlock(BlockKind.TEXT, _box(x), text, confidence)


def _page(
    blocks: tuple[BackendBlock, ...], *, required: bool = False
) -> BackendPage:
    return BackendPage(1, 100, 100, CoordinateUnit.POINTS, blocks, required)


def _document(pages: tuple[BackendPage, ...], name: str) -> BackendDocument:
    evidence = RecognitionEvidence(name, f"{name}-v1", "a" * 64)
    return BackendDocument(pages, evidence, 5)


class _Router:
    def __init__(
        self, native: BackendDocument, ocr: BackendDocument | None = None,
    ) -> None:
        self.native = native
        self.ocr = ocr

    def extract_native(self, data: bytes, limits: PdfLimits) -> BackendDocument:
        return self.native

    def extract_ocr(
        self,
        source: bytes,
        page_numbers: tuple[int, ...],
        limits: PdfLimits,
    ) -> BackendDocument:
        if self.ocr is None:
            raise AssertionError("unexpected OCR")
        return self.ocr


def _text_payload(page: int = 1) -> dict[str, object]:
    return {
        "pages": {str(page): {"size": {"width": 100, "height": 100}}},
        "texts": [{
            "text": "physical text",
            "prov": [{
                "page_no": page,
                "bbox": {"l": 1, "t": 1, "r": 30, "b": 10},
            }],
        }],
    }


def test_runtime_is_fixed_offline_and_uses_verified_rapidocr(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path,
) -> None:
    root, hashes, backend = _model_backend(tmp_path)
    captured = _fake_runtime(monkeypatch)
    backend._runtime(True)
    pipeline = captured["pipeline"]
    rapid = captured["rapid"]
    assert isinstance(pipeline, dict) and isinstance(rapid, dict)
    assert pipeline["enable_remote_services"] is False
    assert pipeline["allow_external_plugins"] is False
    layout = pipeline["layout_options"]
    assert isinstance(layout, dict)
    assert layout["preset"] == "layout_heron_default"
    assert type(layout["engine"]).__name__ == "OnnxEngine"
    assert pipeline["ocr_options"] is not None
    assert rapid["backend"] == "onnxruntime"
    assert rapid["force_full_page_ocr"] is True
    assert rapid["det_model_path"] == str(root / MODEL_PATHS["det"])
    evidence = backend._evidence(True, False)
    assert _tree_digest(hashes) in evidence.model_version
    assert evidence.model_version.startswith("rapidocr/3.9.2@sha256:")


@pytest.mark.parametrize(
    ("package", "version"),
    (("docling", "2.120.0"), ("rapidocr", "3.9.1")),
)
def test_runtime_rejects_version_drift(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    package: str,
    version: str,
) -> None:
    _, _, backend = _model_backend(tmp_path)
    expected = {"docling": "2.121.0", "rapidocr": "3.9.2"}
    expected[package] = version
    monkeypatch.setattr(
        docling_module.importlib.metadata, "version",
        lambda name: expected[name],
    )
    with pytest.raises(PdfExtractionError) as caught:
        backend._runtime(False)
    assert caught.value.code == "NOT_CONFIGURED"


def test_runtime_reports_missing_package(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path,
) -> None:
    _, _, backend = _model_backend(tmp_path)

    def missing(_: str) -> str:
        raise importlib.metadata.PackageNotFoundError("docling")

    monkeypatch.setattr(docling_module.importlib.metadata, "version", missing)
    with pytest.raises(PdfExtractionError) as caught:
        backend._runtime(False)
    assert caught.value.code == "NOT_CONFIGURED"


def test_forced_page_requires_one_matching_physical_page() -> None:
    good = _text_payload(2)
    assert parse_docling_payload(good, ocr=True, forced_page=2)[0].number == 2
    multiple = _text_payload(2)
    multiple["pages"]["3"] = {"size": {"width": 100, "height": 100}}
    wrong_provenance = _text_payload(2)
    wrong_provenance["texts"][0]["prov"][0]["page_no"] = 1
    for payload in (multiple, wrong_provenance, _text_payload(1)):
        with pytest.raises(PdfExtractionError) as caught:
            parse_docling_payload(payload, ocr=True, forced_page=2)
        assert caught.value.code == "PDF_CORRUPT"


def test_out_of_page_bbox_is_rejected() -> None:
    payload = _text_payload()
    payload["texts"][0]["prov"][0]["bbox"]["r"] = 101
    with pytest.raises(PdfExtractionError) as caught:
        parse_docling_payload(payload, ocr=False)
    assert caught.value.code == "PDF_CORRUPT"


def test_missing_cell_bbox_preserves_text_and_requires_review() -> None:
    payload = {
        "pages": {"1": {"size": {"width": 100, "height": 100}}},
        "tables": [{
            "prov": [{
                "page_no": 1,
                "bbox": {"l": 5, "t": 5, "r": 90, "b": 50},
            }],
            "data": {"num_rows": 1, "num_cols": 1, "table_cells": [{
                "text": "knowledge preserved for review",
            }]},
        }],
    }
    page = parse_docling_payload(payload, ocr=False)[0]
    block = page.blocks[0]
    assert block.kind is BlockKind.TEXT
    assert block.text == "knowledge preserved for review"
    assert block.confidence == 0 and block.tables == ()
    native = _document((page,), "native")
    result = PdfDocumentExtractor(_Router(native)).extract(PDF)
    assert result.quality is QualityStatus.REVIEW_REQUIRED


def test_mixed_ocr_deduplicates_normalized_text_by_iou() -> None:
    native_text = "ＡＢＣ  知识库原生文字文本内容"
    ocr_text = "abc 知识库原生文字文本内容"
    native = _document(
        (_page((_block(native_text, 1),), required=True),),
        "native",
    )
    ocr = _document(
        (_page((
            _block(ocr_text, 0.99),
            _block(ocr_text, 0.99, x=55),
        )),),
        "ocr",
    )
    result = PdfDocumentExtractor(_Router(native, ocr)).extract(PDF)
    assert result.mode is ExtractionMode.MIXED
    assert [block.text for block in result.pages[0].blocks] == [
        native_text,
        ocr_text,
    ]
    assert result.pages[0].blocks[1].coordinates.x == 55
    assert [item.mode for item in result.rounds] == [
        ExtractionMode.NATIVE_TEXT,
        ExtractionMode.OCR,
    ]


def test_blank_rapid_round_without_secondary_fails_closed() -> None:
    native = _document(
        (_page((_block("native searchable text", 1),), required=True),),
        "native",
    )
    ocr = _document((_page(()),), "ocr")
    with pytest.raises(PdfExtractionError) as caught:
        PdfDocumentExtractor(_Router(native, ocr)).extract(PDF)
    assert caught.value.code == "NOT_CONFIGURED"


@pytest.mark.parametrize("attribute", ("__cause__", "__context__"))
def test_encryption_is_found_in_exception_chain(attribute: str) -> None:
    outer = RuntimeError("conversion wrapper")
    setattr(outer, attribute, ValueError("password encrypted input"))
    mapped = DoclingBackend._mapped_error(outer)
    assert mapped.code == "PDF_ENCRYPTED"
