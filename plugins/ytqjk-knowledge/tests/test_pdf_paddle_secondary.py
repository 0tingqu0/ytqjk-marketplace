from __future__ import annotations

import base64
import sys
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.image_ocr_backend import (  # noqa: E402
    OcrBackendResult,
    OcrEngineEvidence,
    OcrPoint,
    OcrTextBlock,
)
from scripts.intake_extraction_contracts import (  # noqa: E402
    BlockKind,
    BoundingBox,
    CoordinateUnit,
    ImageClassification,
    LOW_CONFIDENCE_REASON,
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
from scripts.pdf_paddle_backend import (  # noqa: E402
    PaddlePdfSecondaryBackend,
    PdfiumPageRenderer,
    RenderedPdfPage,
)
from scripts.pdf_result_policy import (  # noqa: E402
    OCR_CONFIDENCE_UNREPORTED,
    SECONDARY_OCR_NOT_CONFIGURED,
)
from scripts.pdf_secondary_policy import (  # noqa: E402
    STRUCTURE_USED,
    retry_page_numbers,
)
from scripts.paddle_structure_v3_backend import (  # noqa: E402
    MODEL_KEYS,
    PaddleStructureV3Backend,
)


PDF = b"%PDF-1.7\nfixture"
PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lE"
    "QVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
)


def _evidence(engine: str) -> RecognitionEvidence:
    return RecognitionEvidence(engine, f"{engine}-1", "a" * 64)


def _block(
    text: str,
    score: float,
    kind: BlockKind = BlockKind.TEXT,
    classification: ImageClassification | None = None,
) -> BackendBlock:
    box = BoundingBox(0, 0, 50, 20, CoordinateUnit.POINTS)
    return BackendBlock(
        kind, box, text, score,
        image_classification=classification,
    )


def _page(
    blocks: tuple[BackendBlock, ...],
    *,
    complex_layout: bool = False,
) -> BackendPage:
    return BackendPage(
        1,
        100,
        100,
        CoordinateUnit.POINTS,
        blocks,
        ocr_required=True,
        picture_present=any(
            item.kind is BlockKind.IMAGE for item in blocks
        ),
        complex_layout=complex_layout,
    )


def _document(page: BackendPage, engine: str) -> BackendDocument:
    return BackendDocument((page,), _evidence(engine), 5)


class DoclingFake:
    def __init__(
        self, native: BackendDocument, rapid: BackendDocument,
    ) -> None:
        self.native = native
        self.rapid = rapid

    def extract_native(self, _source, _limits):
        return self.native

    def extract_ocr(self, _source, _numbers, _limits):
        return self.rapid


class SecondaryFake:
    def __init__(self, result: BackendDocument) -> None:
        self.result = result
        self.calls = []

    def extract_secondary(
        self, _source, numbers, _native, complex_pages, _limits,
    ):
        self.calls.append((numbers, complex_pages))
        return self.result


def test_low_confidence_rapid_uses_paddle_result() -> None:
    native = _document(_page(()), "docling")
    rapid = _document(_page((_block("text", 0.80),)), "rapid")
    paddle = _document(_page((_block("text", 0.99),)), "paddle")
    secondary = SecondaryFake(paddle)
    result = PdfDocumentExtractor(
        DoclingFake(native, rapid),
        secondary_backend=secondary,
    ).extract(PDF)
    assert secondary.calls == [((1,), ())]
    assert result.pages[0].blocks[0].confidence == 0.99
    assert result.pages[0].blocks[0].evidence.engine == "paddle"
    assert result.quality is QualityStatus.ACCEPTABLE


def test_unreported_confidence_keeps_text_for_manual_review() -> None:
    page = _page((_block("text without reported confidence", 0.0),))
    assert retry_page_numbers((page,), (), 0.88) == ()
    assert retry_page_numbers((page,), (1,), 0.88) == (1,)
    secondary = SecondaryFake(_document(page, "paddle"))
    result = PdfDocumentExtractor(
        DoclingFake(_document(_page(()), "docling"),
                    _document(page, "rapid")),
        secondary_backend=secondary,
    ).extract(PDF)
    assert secondary.calls == []
    assert LOW_CONFIDENCE_REASON in result.review_reasons
    assert OCR_CONFIDENCE_UNREPORTED in result.review_reasons
    assert SECONDARY_OCR_NOT_CONFIGURED not in result.review_reasons


def test_complex_page_keeps_picture_semantics() -> None:
    classification = ImageClassification(
        "diagram",
        ("diagram", "workflow"),
        "上传后进入人工复核流程。",
        0.99,
        _evidence("smolvlm"),
    )
    image = _block(
        classification.summary,
        0.99,
        BlockKind.IMAGE,
        classification,
    )
    native = _document(_page((image,), complex_layout=True), "docling")
    rapid = _document(_page((_block("rapid", 0.99),)), "rapid")
    paddle = _document(_page((_block("table cells", 0.98),)), "structure")
    result = PdfDocumentExtractor(
        DoclingFake(native, rapid),
        secondary_backend=SecondaryFake(paddle),
    ).extract(PDF)
    blocks = result.pages[0].blocks
    assert blocks[0].image_classification == classification
    assert blocks[1].text == "table cells"
    assert STRUCTURE_USED in result.review_reasons


class RendererFake:
    def render(self, _source: bytes, number: int) -> RenderedPdfPage:
        return RenderedPdfPage(number, PNG, 1, 1, 2, "5.13.0")


class OcrFake:
    def recognize(self, _source: bytes) -> OcrBackendResult:
        points = (
            OcrPoint(0, 0), OcrPoint(1, 0),
            OcrPoint(1, 1), OcrPoint(0, 1),
        )
        evidence = OcrEngineEvidence(
            "paddleocr", "3.7.0", "a" * 64, "b" * 64,
        )
        return OcrBackendResult(
            1, 1, (OcrTextBlock("OCR", points, 0.99),), 3, evidence,
        )


def test_pdf_paddle_backend_scales_boxes_and_records_evidence() -> None:
    backend = PaddlePdfSecondaryBackend(
        OcrFake(), renderer=RendererFake(),
    )
    result = backend.extract_secondary(
        PDF, (1,), (_page(()),), (), PdfLimits(),
    )
    block = result.pages[0].blocks[0]
    assert (block.coordinates.width, block.coordinates.height) == (100, 100)
    assert result.pages[0].evidence.engine == "paddleocr-pdf"
    assert result.evidence.engine == "paddle-pdf-secondary"


def test_complex_page_without_structure_models_is_not_configured() -> None:
    backend = PaddlePdfSecondaryBackend(
        OcrFake(), renderer=RendererFake(),
    )
    with pytest.raises(PdfExtractionError) as caught:
        backend.extract_secondary(
            PDF, (1,), (_page(()),), (1,), PdfLimits(),
        )
    assert caught.value.code == "NOT_CONFIGURED"


def test_pdfium_version_drift_is_not_configured() -> None:
    renderer = PdfiumPageRenderer(
        module_loader=lambda: object(),
        version_getter=lambda: "5.12.0",
    )
    with pytest.raises(PdfExtractionError) as caught:
        renderer.render(PDF, 1)
    assert caught.value.code == "NOT_CONFIGURED"


def test_structure_pipeline_is_cpu_only_and_offline(
    tmp_path: Path,
) -> None:
    paths = {}
    for key in MODEL_KEYS:
        model = tmp_path / key
        model.mkdir()
        (model / "inference.pdiparams").write_bytes(b"model")
        paths[key] = model
    captured = {}

    class Result:
        json = {
            "res": {
                "parsing_res_list": [
                    {
                        "block_content": "table",
                        "block_bbox": [0, 0, 1, 1],
                        "block_score": 0.99,
                    }
                ]
            }
        }

    class Pipeline:
        @staticmethod
        def predict(_image):
            return [Result()]

    def factory(**kwargs):
        captured.update(kwargs)
        return Pipeline()

    backend = PaddleStructureV3Backend(
        paths,
        pipeline_factory=factory,
        version_getter=lambda: "3.7.0",
    )
    assert backend.analyze(PNG).blocks[0].text == "table"
    assert captured["device"] == "cpu"
    disabled = {
        key: value
        for key, value in captured.items()
        if key.startswith("use_")
    }
    assert disabled
    assert set(disabled.values()) == {False}
