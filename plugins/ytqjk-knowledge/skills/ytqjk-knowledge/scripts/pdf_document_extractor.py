"""Accuracy-first PDF routing independent from one extraction engine."""

from __future__ import annotations

import hashlib
from dataclasses import dataclass
from typing import Protocol

from scripts.intake_extraction_contracts import (
    BlockKind, BoundingBox, CONFIDENCE_THRESHOLD,
    CoordinateUnit, ExtractedBlock, ExtractedPage,
    ExtractedTable, ExtractionMode, ExtractionResult,
    ImageClassification, QualityStatus, RecognitionEvidence,
    RecognitionRound,
)
from scripts.pdf_document_materializer import (
    DEDUP_IOU_THRESHOLD,
    extraction_mode,
    materialize_page,
    page_evidence,
    validate_pages,
    visible_characters,
)
from scripts.pdf_result_policy import (
    PICTURE_DESCRIPTION_NOT_CONFIGURED,
    PICTURE_DESCRIPTION_UNVERIFIED,
    SECONDARY_OCR_NOT_CONFIGURED,
    configuration_digest,
    review_reasons,
)
from scripts.pdf_secondary_policy import (
    choose_secondary_pages,
    retry_page_numbers,
)

class PdfExtractionError(RuntimeError):
    def __init__(self, code: str, message: str) -> None:
        super().__init__(message)
        self.code = code


@dataclass(frozen=True)
class PdfLimits:
    max_bytes: int = 50 * 1024 * 1024
    max_pages: int = 200
    native_min_characters: int = 12
    review_threshold: float = 0.88

    def __post_init__(self) -> None:
        if self.max_bytes < 1 or self.max_pages < 1:
            raise ValueError("PDF limits must be positive")
        if self.native_min_characters < 1:
            raise ValueError("native_min_characters must be positive")
        if not CONFIDENCE_THRESHOLD <= self.review_threshold <= 1:
            raise ValueError("review_threshold must be in [0.88, 1]")


@dataclass(frozen=True)
class BackendBlock:
    kind: BlockKind
    coordinates: BoundingBox
    text: str
    confidence: float
    tables: tuple[ExtractedTable, ...] = ()
    image_classification: ImageClassification | None = None


@dataclass(frozen=True)
class BackendPage:
    number: int
    width: float
    height: float
    coordinate_unit: CoordinateUnit
    blocks: tuple[BackendBlock, ...]
    ocr_required: bool = False
    picture_present: bool = False
    complex_layout: bool = False
    evidence: RecognitionEvidence | None = None


@dataclass(frozen=True)
class BackendDocument:
    pages: tuple[BackendPage, ...]
    evidence: RecognitionEvidence
    elapsed_ms: int


class PdfExtractionBackend(Protocol):
    def extract_native(
        self, source: bytes, limits: PdfLimits
    ) -> BackendDocument:
        ...


class PdfSecondaryBackend(Protocol):
    def extract_secondary(
        self,
        source: bytes,
        page_numbers: tuple[int, ...],
        native_pages: tuple[BackendPage, ...],
        complex_pages: tuple[int, ...],
        limits: PdfLimits,
    ) -> BackendDocument:
        ...

    def extract_ocr(
        self,
        source: bytes,
        page_numbers: tuple[int, ...],
        limits: PdfLimits,
    ) -> BackendDocument:
        ...


class PdfDocumentExtractor:
    def __init__(
        self,
        backend: PdfExtractionBackend,
        limits: PdfLimits | None = None,
        *,
        secondary_backend: PdfSecondaryBackend | None = None,
    ) -> None:
        self._backend = backend
        self._limits = limits or PdfLimits()
        self._secondary = secondary_backend

    def extract(self, source: bytes) -> ExtractionResult:
        self._preflight(source)
        native = self._backend.extract_native(source, self._limits)
        validate_pages(native, None, self._limits)
        scan_numbers = tuple(
            page.number
            for page in native.pages
            if page.ocr_required
            or visible_characters(page)
            < self._limits.native_min_characters
        )
        ocr = None
        if scan_numbers:
            ocr = self._backend.extract_ocr(
                source, scan_numbers, self._limits
            )
            validate_pages(ocr, scan_numbers, self._limits)
            native_by_number = {
                page.number: page for page in native.pages
            }
            for page in ocr.pages:
                original = native_by_number[page.number]
                if (
                    page.width != original.width
                    or page.height != original.height
                    or page.coordinate_unit is not original.coordinate_unit
                ):
                    raise PdfExtractionError(
                        "BACKEND_FAILURE",
                        "OCR page geometry does not match native page",
                    )
        complex_numbers = tuple(
            page.number for page in native.pages if page.complex_layout
        )
        secondary = None
        extra_reasons: tuple[str, ...] = ()
        selected_pages = (
            {page.number: page for page in ocr.pages} if ocr else {}
        )
        if ocr is not None:
            requested = retry_page_numbers(
                ocr.pages,
                complex_numbers,
                self._limits.review_threshold,
            )
            retry = requested
            if retry and self._secondary is None:
                raise PdfExtractionError(
                    "NOT_CONFIGURED",
                    "Paddle PDF secondary recognition is unavailable",
                )
            if retry:
                secondary = self._secondary.extract_secondary(
                    source,
                    retry,
                    native.pages,
                    tuple(number for number in retry
                          if number in set(complex_numbers)),
                    self._limits,
                )
                validate_pages(secondary, retry, self._limits)
                decision = choose_secondary_pages(
                    ocr,
                    secondary,
                    complex_numbers,
                    self._limits.review_threshold,
                )
                selected_pages = decision.pages
                extra_reasons = decision.reasons
        pages = tuple(
            materialize_page(
                page,
                selected_pages.get(page.number),
                page.number not in scan_numbers
                or visible_characters(page)
                >= self._limits.native_min_characters,
                native.evidence,
                page_evidence(
                    selected_pages.get(page.number), ocr, secondary,
                ),
            )
            for page in native.pages
        )
        low_confidence = any(
            block.confidence < self._limits.review_threshold
            for page in pages
            for block in page.blocks
        )
        picture_present = any(page.picture_present for page in native.pages)
        picture_described = any(
            block.image_classification is not None
            for page in pages
            for block in page.blocks
        )
        confidence_unreported = bool(ocr) and any(
            page.blocks
            and all(block.confidence == 0 for block in page.blocks)
            for page in ocr.pages
        )
        reasons = review_reasons(
            low_confidence,
            picture_present,
            picture_described,
            secondary_attempted=secondary is not None,
            confidence_unreported=confidence_unreported,
        )
        reasons = tuple(dict.fromkeys((*reasons, *extra_reasons)))
        quality = (
            QualityStatus.REVIEW_REQUIRED
            if reasons
            else QualityStatus.ACCEPTABLE
        )
        mode = extraction_mode(pages)
        rounds = self._rounds(mode, native, ocr, secondary)
        return ExtractionResult(
            hashlib.sha256(source).hexdigest(),
            configuration_digest(
                self._limits,
                DEDUP_IOU_THRESHOLD,
                native.evidence.config_digest,
                ocr.evidence.config_digest if ocr else None,
                secondary.evidence.config_digest if secondary else None,
            ),
            mode,
            rounds,
            pages,
            quality,
            tuple(reasons),
            native.elapsed_ms + (ocr.elapsed_ms if ocr else 0)
            + (secondary.elapsed_ms if secondary else 0),
        )

    def _preflight(self, source: bytes) -> None:
        if not isinstance(source, bytes) or not source.startswith(b"%PDF-"):
            raise PdfExtractionError("PDF_CORRUPT", "invalid PDF signature")
        if len(source) > self._limits.max_bytes:
            raise PdfExtractionError("PDF_TOO_LARGE", "PDF size limit exceeded")

    @staticmethod
    def _rounds(
        mode: ExtractionMode,
        native: BackendDocument,
        ocr: BackendDocument | None,
        secondary: BackendDocument | None,
    ) -> tuple[RecognitionRound, ...]:
        if mode is ExtractionMode.NATIVE_TEXT:
            return (
                RecognitionRound(1, mode, native.evidence, native.elapsed_ms),
            )
        if mode is ExtractionMode.OCR:
            if ocr is None:
                raise PdfExtractionError(
                    "BACKEND_FAILURE", "OCR round is missing"
                )
            rounds = [
                RecognitionRound(1, mode, ocr.evidence, ocr.elapsed_ms)
            ]
            if secondary is not None:
                rounds.append(RecognitionRound(
                    2, mode, secondary.evidence, secondary.elapsed_ms,
                ))
            return tuple(rounds)
        if ocr is None:
            raise PdfExtractionError("BACKEND_FAILURE", "mixed OCR is missing")
        rounds = [
            RecognitionRound(
                1,
                ExtractionMode.NATIVE_TEXT,
                native.evidence,
                native.elapsed_ms,
            ),
            RecognitionRound(
                2, ExtractionMode.OCR, ocr.evidence, ocr.elapsed_ms
            ),
        ]
        if secondary is not None:
            rounds.append(RecognitionRound(
                3,
                ExtractionMode.OCR,
                secondary.evidence,
                secondary.elapsed_ms,
            ))
        return tuple(rounds)
