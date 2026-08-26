from __future__ import annotations

import sys
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.docling_backend import DoclingBackend  # noqa: E402
from scripts.docling_payload_parser import (  # noqa: E402
    parse_docling_payload,
)
from scripts.intake_extraction_contracts import (  # noqa: E402
    BlockKind,
    BoundingBox,
    CoordinateUnit,
    ExtractedTable,
    ExtractionMode,
    QualityStatus,
    RecognitionEvidence,
    TableCell,
)
from scripts.pdf_document_extractor import (  # noqa: E402
    BackendBlock,
    BackendDocument,
    BackendPage,
    PdfDocumentExtractor,
    PdfExtractionError,
    PdfLimits,
    PICTURE_DESCRIPTION_NOT_CONFIGURED,
    SECONDARY_OCR_NOT_CONFIGURED,
)


PDF = b"%PDF-1.7\nfixture"
DIGEST_A = "a" * 64
DIGEST_B = "b" * 64


def _evidence(name: str = "native") -> RecognitionEvidence:
    digest = DIGEST_A if name == "native" else DIGEST_B
    return RecognitionEvidence(name, f"{name}-v1", digest)


def _box(x: float = 1, y: float = 1) -> BoundingBox:
    return BoundingBox(x, y, 20, 10, CoordinateUnit.POINTS)


def _block(
    text: str,
    confidence: float = 1,
    kind: BlockKind = BlockKind.TEXT,
    tables: tuple[ExtractedTable, ...] = (),
) -> BackendBlock:
    return BackendBlock(kind, _box(), text, confidence, tables)


def _page(
    number: int,
    blocks: tuple[BackendBlock, ...],
    *,
    ocr_required: bool = False,
    picture_present: bool = False,
    complex_layout: bool = False,
) -> BackendPage:
    return BackendPage(
        number,
        100,
        100,
        CoordinateUnit.POINTS,
        blocks,
        ocr_required,
        picture_present,
        complex_layout,
    )


def _document(
    pages: tuple[BackendPage, ...],
    name: str = "native",
    elapsed: int = 5,
) -> BackendDocument:
    return BackendDocument(pages, _evidence(name), elapsed)


class FakeBackend:
    def __init__(
        self,
        native: BackendDocument,
        ocr: BackendDocument | None = None,
    ) -> None:
        self.native = native
        self.ocr = ocr
        self.ocr_calls: list[tuple[int, ...]] = []

    def extract_native(
        self, source: bytes, limits: PdfLimits
    ) -> BackendDocument:
        return self.native

    def extract_ocr(
        self,
        source: bytes,
        page_numbers: tuple[int, ...],
        limits: PdfLimits,
    ) -> BackendDocument:
        self.ocr_calls.append(page_numbers)
        if self.ocr is None:
            raise AssertionError("unexpected OCR")
        selected = tuple(
            page for page in self.ocr.pages if page.number in page_numbers
        )
        return BackendDocument(
            selected, self.ocr.evidence, self.ocr.elapsed_ms
        )


def test_native_pdf_skips_ocr_and_keeps_evidence() -> None:
    cell = TableCell(0, 0, "42", _box())
    table = ExtractedTable("table-1", _box(), 1, 1, (cell,))
    backend = FakeBackend(
        _document((_page(1, (_block(
            "native searchable text", kind=BlockKind.TABLE,
            tables=(table,),
        ),)),))
    )
    result = PdfDocumentExtractor(backend).extract(PDF)
    block = result.pages[0].blocks[0]
    assert result.mode is ExtractionMode.NATIVE_TEXT
    assert result.quality is QualityStatus.ACCEPTABLE
    assert block.evidence.engine == "native"
    assert block.tables[0].cells[0].text == "42"
    assert block.coordinates.unit is CoordinateUnit.POINTS
    assert backend.ocr_calls == []


def test_usable_native_complex_layout_skips_redundant_ocr() -> None:
    page = _page(
        1,
        (_block("native two-column content"),),
        complex_layout=True,
    )
    backend = FakeBackend(_document((page,)))
    result = PdfDocumentExtractor(backend).extract(PDF)
    assert result.mode is ExtractionMode.NATIVE_TEXT
    assert backend.ocr_calls == []


def test_scanned_pdf_ocrs_only_empty_page() -> None:
    native = _document((_page(1, ()),))
    ocr = _document((_page(1, (_block("扫描文本", 0.98),)),), "ocr", 12)
    backend = FakeBackend(native, ocr)
    result = PdfDocumentExtractor(backend).extract(PDF)
    assert backend.ocr_calls == [(1,)]
    assert result.mode is ExtractionMode.OCR
    assert result.pages[0].mode is ExtractionMode.OCR
    assert [item.evidence.engine for item in result.rounds] == ["ocr"]


def test_mixed_page_merges_native_and_requested_ocr() -> None:
    native = _document((
        _page(1, (_block("first native page"),)),
        _page(
            2,
            (_block("second native text"),),
            ocr_required=True,
        ),
    ))
    ocr = _document((_page(2, (_block("image text", 0.97),)),), "ocr")
    backend = FakeBackend(native, ocr)
    result = PdfDocumentExtractor(backend).extract(PDF)
    assert backend.ocr_calls == [(2,)]
    assert result.mode is ExtractionMode.MIXED
    assert result.pages[0].mode is ExtractionMode.NATIVE_TEXT
    assert result.pages[1].mode is ExtractionMode.MIXED
    assert [block.mode for block in result.pages[1].blocks] == [
        ExtractionMode.NATIVE_TEXT,
        ExtractionMode.OCR,
    ]


def test_low_confidence_without_secondary_fails_closed() -> None:
    native = _document((_page(1, ()),))
    ocr = _document((_page(1, (_block("uncertain", 0.879),)),), "ocr")
    with pytest.raises(PdfExtractionError) as caught:
        PdfDocumentExtractor(FakeBackend(native, ocr)).extract(PDF)
    assert caught.value.code == "NOT_CONFIGURED"


def test_pdf_picture_description_is_explicitly_not_configured() -> None:
    page = _page(
        1,
        (_block("native searchable text"),),
        picture_present=True,
    )
    result = PdfDocumentExtractor(FakeBackend(_document((page,)))).extract(PDF)
    assert result.quality is QualityStatus.REVIEW_REQUIRED
    assert result.review_reasons == (
        PICTURE_DESCRIPTION_NOT_CONFIGURED,
    )


@pytest.mark.parametrize(
    ("source", "limits", "code"),
    (
        (b"not-pdf", PdfLimits(), "PDF_CORRUPT"),
        (PDF, PdfLimits(max_bytes=5), "PDF_TOO_LARGE"),
    ),
)
def test_preflight_limits_fail_closed(
    source: bytes, limits: PdfLimits, code: str
) -> None:
    backend = FakeBackend(_document((_page(1, (_block("text"),)),)))
    with pytest.raises(PdfExtractionError) as caught:
        PdfDocumentExtractor(backend, limits).extract(source)
    assert caught.value.code == code


def test_backend_encryption_and_page_limit_fail_closed() -> None:
    class EncryptedBackend(FakeBackend):
        def extract_native(
            self, source: bytes, limits: PdfLimits
        ) -> BackendDocument:
            raise PdfExtractionError("PDF_ENCRYPTED", "password required")

    encrypted = EncryptedBackend(_document((_page(1, ()),)))
    with pytest.raises(PdfExtractionError) as caught:
        PdfDocumentExtractor(encrypted).extract(PDF)
    assert caught.value.code == "PDF_ENCRYPTED"
    pages = tuple(_page(number, (_block("text"),)) for number in range(1, 4))
    with pytest.raises(PdfExtractionError) as caught:
        PdfDocumentExtractor(
            FakeBackend(_document(pages)), PdfLimits(max_pages=2)
        ).extract(PDF)
    assert caught.value.code == "PDF_TOO_MANY_PAGES"


def test_geometry_mismatch_is_not_silently_merged() -> None:
    native = _document((_page(1, ()),))
    wrong = BackendPage(
        1,
        200,
        100,
        CoordinateUnit.POINTS,
        (_block("ocr", 0.99),),
    )
    with pytest.raises(PdfExtractionError) as caught:
        PdfDocumentExtractor(
            FakeBackend(native, _document((wrong,), "ocr"))
        ).extract(PDF)
    assert caught.value.code == "BACKEND_FAILURE"


def test_docling_adapter_is_lazy_and_reports_not_configured() -> None:
    backend = DoclingBackend()
    with pytest.raises(PdfExtractionError) as caught:
        backend.extract_native(PDF, PdfLimits())
    assert caught.value.code == "NOT_CONFIGURED"


def test_docling_payload_preserves_table_cells_and_scan_signal() -> None:
    payload = {
        "pages": {"1": {"size": {"width": 100, "height": 100}}},
        "texts": [{
            "self_ref": "#/texts/0",
            "text": "heading",
            "prov": [{"page_no": 1, "bbox": {
                "l": 1, "t": 1, "r": 30, "b": 10,
            }}],
        }],
        "tables": [{
            "self_ref": "#/tables/0",
            "confidence": 0.96,
            "prov": [{"page_no": 1, "bbox": {
                "l": 10, "t": 20, "r": 90, "b": 60,
            }}],
            "data": {
                "num_rows": 1,
                "num_cols": 1,
                "table_cells": [{
                    "start_row_offset_idx": 0,
                    "end_row_offset_idx": 1,
                    "start_col_offset_idx": 0,
                    "end_col_offset_idx": 1,
                    "text": "cell",
                    "bbox": {
                        "l": 15, "t": 25, "r": 85, "b": 55,
                    },
                }],
            },
        }],
        "body": {"children": [
            {"$ref": "#/tables/0"},
            {"$ref": "#/texts/0"},
        ]},
        "pictures": [{"prov": [{"page_no": 1, "bbox": {
            "l": 0, "t": 0, "r": 60, "b": 60,
        }}]}],
    }
    page = parse_docling_payload(payload, ocr=False)[0]
    table = page.blocks[0]
    assert page.ocr_required is True
    assert page.picture_present is True
    assert table.kind is BlockKind.TABLE
    assert table.confidence == 0.96
    assert table.tables[0].cells[0].text == "cell"
    coordinates = table.tables[0].cells[0].coordinates
    assert coordinates.unit is CoordinateUnit.POINTS
    assert (coordinates.x, coordinates.y) == (15, 25)
    assert page.blocks[1].text == "heading"
    with pytest.raises(PdfExtractionError) as caught:
        parse_docling_payload({"pages": {"bad": {}}}, ocr=False)
    assert caught.value.code == "PDF_CORRUPT"
