"""Validate and materialize page-level PDF extraction results."""

from __future__ import annotations

import unicodedata
from typing import TYPE_CHECKING, Any

from scripts.intake_extraction_contracts import (
    BlockKind,
    BoundingBox,
    ExtractedBlock,
    ExtractedPage,
    ExtractionMode,
    RecognitionEvidence,
)

if TYPE_CHECKING:
    from scripts.pdf_document_extractor import (
        BackendDocument,
        BackendPage,
        PdfLimits,
    )
else:
    BackendDocument = Any
    BackendPage = Any
    PdfLimits = Any


DEDUP_IOU_THRESHOLD = 0.5


def visible_characters(page: BackendPage) -> int:
    return sum(len("".join(block.text.split())) for block in page.blocks)


def extraction_mode(
    blocks: tuple[ExtractedBlock, ...] | tuple[ExtractedPage, ...],
) -> ExtractionMode:
    modes = {block.mode for block in blocks}
    return next(iter(modes)) if len(modes) == 1 else ExtractionMode.MIXED


def validate_pages(
    document: BackendDocument,
    expected: tuple[int, ...] | None,
    limits: PdfLimits,
) -> None:
    from scripts.pdf_document_extractor import PdfExtractionError

    numbers = tuple(page.number for page in document.pages)
    if not numbers:
        raise PdfExtractionError("PDF_CORRUPT", "PDF contains no pages")
    if len(numbers) > limits.max_pages:
        raise PdfExtractionError(
            "PDF_TOO_MANY_PAGES", "PDF page limit exceeded"
        )
    required = expected or tuple(range(1, len(numbers) + 1))
    if numbers != required:
        raise PdfExtractionError(
            "INVALID_PAGE_SEQUENCE", "backend returned wrong physical pages"
        )


def materialize_page(
    native: BackendPage,
    ocr: BackendPage | None,
    use_native: bool,
    native_evidence: RecognitionEvidence,
    ocr_evidence: RecognitionEvidence | None,
) -> ExtractedPage:
    from scripts.pdf_document_extractor import PdfExtractionError

    sources: list[
        tuple[object, ExtractionMode, RecognitionEvidence]
    ] = []
    if ocr is not None and (
        ocr.width != native.width
        or ocr.height != native.height
        or ocr.coordinate_unit is not native.coordinate_unit
    ):
        raise PdfExtractionError(
            "BACKEND_FAILURE", "OCR page geometry does not match native page"
        )
    native_blocks = native.blocks if use_native else tuple(
        item
        for item in native.blocks
        if item.kind in {BlockKind.TABLE, BlockKind.IMAGE}
    )
    sources.extend(
        (item, ExtractionMode.NATIVE_TEXT, native_evidence)
        for item in native_blocks
    )
    if ocr is not None:
        if ocr_evidence is None:
            raise PdfExtractionError(
                "BACKEND_FAILURE", "OCR evidence is missing"
            )
        ocr_blocks = _unique_ocr(
            native.blocks if use_native else (), ocr
        )
        sources.extend(
            (item, ExtractionMode.OCR, ocr_evidence)
            for item in ocr_blocks
        )
    blocks = tuple(
        ExtractedBlock(
            block_id=f"page-{native.number}-block-{ordinal}",
            page_number=native.number,
            ordinal=ordinal,
            kind=item.kind,
            coordinates=item.coordinates,
            text=item.text,
            mode=mode,
            confidence=item.confidence,
            evidence=evidence,
            tables=item.tables,
            image_classification=item.image_classification,
        )
        for ordinal, (item, mode, evidence) in enumerate(sources, 1)
    )
    return ExtractedPage(
        native.number,
        native.width,
        native.height,
        native.coordinate_unit,
        extraction_mode(blocks),
        blocks,
    )


def page_evidence(
    page: BackendPage | None,
    primary: BackendDocument | None,
    secondary: BackendDocument | None,
) -> RecognitionEvidence | None:
    if page is None:
        return None
    if page.evidence is not None:
        return page.evidence
    if secondary is not None and page in secondary.pages:
        return secondary.evidence
    return primary.evidence if primary is not None else None


def _unique_ocr(
    native: tuple[object, ...], ocr: BackendPage,
) -> tuple[object, ...]:
    unique = tuple(
        item
        for item in ocr.blocks
        if not any(
            _text_key(item.text)
            and _text_key(item.text) == _text_key(other.text)
            and _iou(item.coordinates, other.coordinates)
            >= DEDUP_IOU_THRESHOLD
            for other in native
        )
    )
    if unique:
        return unique
    from scripts.pdf_document_extractor import BackendBlock

    confidence = min((item.confidence for item in ocr.blocks), default=0)
    marker = BackendBlock(
        BlockKind.TEXT,
        BoundingBox(0, 0, ocr.width, ocr.height, ocr.coordinate_unit),
        "",
        confidence,
    )
    return (marker,)


def _text_key(value: str) -> str:
    normalized = unicodedata.normalize("NFKC", value).casefold()
    return " ".join(normalized.split())


def _iou(first: BoundingBox, second: BoundingBox) -> float:
    if first.unit is not second.unit:
        return 0
    left, top = max(first.x, second.x), max(first.y, second.y)
    right = min(first.x + first.width, second.x + second.width)
    bottom = min(first.y + first.height, second.y + second.height)
    intersection = max(right - left, 0) * max(bottom - top, 0)
    union = first.width * first.height + second.width * second.height
    return intersection / (union - intersection) if union > intersection else 0


__all__ = [
    "DEDUP_IOU_THRESHOLD",
    "extraction_mode",
    "materialize_page",
    "page_evidence",
    "validate_pages",
    "visible_characters",
]
