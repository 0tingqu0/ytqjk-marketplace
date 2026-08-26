"""Render selected PDF pages and run local Paddle document models."""

from __future__ import annotations

import hashlib
import importlib.metadata
import json
import math
import time
from dataclasses import dataclass
from io import BytesIO
from typing import Callable, Protocol

from PIL import Image

from scripts.image_input_guard import (
    MAX_IMAGE_PIXELS,
    validate_image_input,
)
from scripts.image_ocr_backend import OcrBackend, OcrBackendResult
from scripts.intake_extraction_contracts import (
    BlockKind,
    BoundingBox,
    CoordinateUnit,
    RecognitionEvidence,
)
from scripts.paddle_structure_v3_backend import StructureResult
from scripts.pdf_document_extractor import (
    BackendBlock,
    BackendDocument,
    BackendPage,
    PdfExtractionError,
    PdfLimits,
)


EXPECTED_PDFIUM_VERSION = "5.13.0"


def _load_pdfium() -> object:
    import pypdfium2 as pdfium

    return pdfium


@dataclass(frozen=True)
class RenderedPdfPage:
    number: int
    image_bytes: bytes
    width: int
    height: int
    elapsed_ms: int
    renderer_version: str


class PageRenderer(Protocol):
    def render(
        self, source: bytes, page_number: int,
    ) -> RenderedPdfPage: ...


class StructureBackend(Protocol):
    def analyze(self, image_bytes: bytes) -> StructureResult: ...


class PdfiumPageRenderer:
    def __init__(
        self,
        scale: float = 2.0,
        *,
        module_loader: Callable[[], object] = _load_pdfium,
        version_getter: Callable[[], str] | None = None,
    ) -> None:
        if not math.isfinite(scale) or not 1 <= scale <= 4:
            raise ValueError("PDF render scale is invalid")
        self._scale = scale
        self._module_loader = module_loader
        self._version_getter = version_getter or (
            lambda: importlib.metadata.version("pypdfium2")
        )

    def render(self, source: bytes, page_number: int) -> RenderedPdfPage:
        try:
            pdfium = self._module_loader()
            version = self._version_getter()
        except (ImportError, importlib.metadata.PackageNotFoundError) as error:
            raise PdfExtractionError(
                "NOT_CONFIGURED", "pypdfium2 renderer is unavailable"
            ) from error
        if version != EXPECTED_PDFIUM_VERSION:
            raise PdfExtractionError(
                "NOT_CONFIGURED",
                "pypdfium2 5.13.0 is required",
            )
        started = time.perf_counter()
        document = None
        page = None
        bitmap = None
        try:
            document = pdfium.PdfDocument(source)
            if page_number < 1 or page_number > len(document):
                raise PdfExtractionError(
                    "INVALID_PAGE_SEQUENCE", "PDF render page is invalid"
                )
            page = document[page_number - 1]
            width, height = page.get_size()
            pixels = math.ceil(width * self._scale) * math.ceil(
                height * self._scale
            )
            if pixels > MAX_IMAGE_PIXELS:
                raise PdfExtractionError(
                    "PDF_PAGE_TOO_LARGE", "rendered PDF page is too large"
                )
            bitmap = page.render(scale=self._scale)
            image = bitmap.to_pil().convert("RGB")
            buffer = BytesIO()
            image.save(buffer, format="PNG")
            data = buffer.getvalue()
            image_width, image_height = validate_image_input(data)
        except PdfExtractionError:
            raise
        except Exception as error:
            raise PdfExtractionError(
                "BACKEND_FAILURE", "PDF page rendering failed"
            ) from error
        finally:
            for item in (bitmap, page, document):
                closer = getattr(item, "close", None)
                if callable(closer):
                    closer()
        elapsed = round((time.perf_counter() - started) * 1000)
        return RenderedPdfPage(
            page_number, data, image_width, image_height, elapsed, version,
        )


class PaddlePdfSecondaryBackend:
    def __init__(
        self,
        ocr_backend: OcrBackend,
        *,
        structure_backend: StructureBackend | None = None,
        renderer: PageRenderer | None = None,
    ) -> None:
        self._ocr = ocr_backend
        self._structure = structure_backend
        self._renderer = renderer or PdfiumPageRenderer()

    def extract_secondary(
        self,
        source: bytes,
        page_numbers: tuple[int, ...],
        native_pages: tuple[BackendPage, ...],
        complex_pages: tuple[int, ...],
        limits: PdfLimits,
    ) -> BackendDocument:
        if not page_numbers or len(page_numbers) > limits.max_pages:
            raise PdfExtractionError(
                "INVALID_PAGE_SEQUENCE", "secondary page range is invalid"
            )
        native = {page.number: page for page in native_pages}
        if set(page_numbers) - set(native):
            raise PdfExtractionError(
                "INVALID_PAGE_SEQUENCE", "secondary page is missing"
            )
        pages = []
        evidences = []
        elapsed = 0
        complex_set = set(complex_pages)
        for number in page_numbers:
            rendered = self._renderer.render(source, number)
            if rendered.number != number:
                raise PdfExtractionError(
                    "INVALID_PAGE_SEQUENCE", "renderer returned wrong page"
                )
            validate_image_input(rendered.image_bytes)
            if number in complex_set:
                page, evidence, inference = self._structure_page(
                    rendered, native[number]
                )
            else:
                page, evidence, inference = self._ocr_page(
                    rendered, native[number]
                )
            pages.append(page)
            evidences.append(evidence)
            elapsed += rendered.elapsed_ms + inference
        aggregate = _aggregate_evidence(evidences)
        return BackendDocument(tuple(pages), aggregate, elapsed)

    def _ocr_page(
        self, rendered: RenderedPdfPage, native: BackendPage,
    ) -> tuple[BackendPage, RecognitionEvidence, int]:
        result = self._ocr.recognize(rendered.image_bytes)
        if type(result) is not OcrBackendResult:
            raise PdfExtractionError(
                "BACKEND_FAILURE", "PaddleOCR returned an invalid result"
            )
        if (result.width, result.height) != (
            rendered.width, rendered.height
        ):
            raise PdfExtractionError(
                "BACKEND_FAILURE", "PaddleOCR page geometry changed"
            )
        evidence = _ocr_evidence(result, rendered.renderer_version)
        blocks = tuple(
            BackendBlock(
                BlockKind.TEXT,
                _ocr_box(item, result, native),
                item.text,
                item.confidence,
            )
            for item in result.blocks
        )
        page = BackendPage(
            native.number, native.width, native.height,
            native.coordinate_unit, blocks, evidence=evidence,
        )
        return page, evidence, result.elapsed_ms

    def _structure_page(
        self, rendered: RenderedPdfPage, native: BackendPage,
    ) -> tuple[BackendPage, RecognitionEvidence, int]:
        if self._structure is None:
            raise PdfExtractionError(
                "NOT_CONFIGURED",
                "PP-StructureV3 models are required for complex PDF pages",
            )
        result = self._structure.analyze(rendered.image_bytes)
        if type(result) is not StructureResult:
            raise PdfExtractionError(
                "BACKEND_FAILURE", "PP-StructureV3 result is invalid"
            )
        if (result.width, result.height) != (
            rendered.width, rendered.height
        ):
            raise PdfExtractionError(
                "BACKEND_FAILURE", "PP-StructureV3 page geometry changed"
            )
        blocks = tuple(
            BackendBlock(
                BlockKind.TEXT,
                _structure_box(item.box, result, native),
                item.text,
                item.confidence,
            )
            for item in result.blocks
        )
        page = BackendPage(
            native.number, native.width, native.height,
            native.coordinate_unit, blocks, evidence=result.evidence,
        )
        return page, result.evidence, result.elapsed_ms


def _ocr_box(
    block: object, result: OcrBackendResult, native: BackendPage,
) -> BoundingBox:
    xs = [point.x for point in block.quad]
    ys = [point.y for point in block.quad]
    box = min(xs), min(ys), max(xs) - min(xs), max(ys) - min(ys)
    return _scaled_box(box, result.width, result.height, native)


def _structure_box(
    box: tuple[float, float, float, float],
    result: StructureResult,
    native: BackendPage,
) -> BoundingBox:
    return _scaled_box(box, result.width, result.height, native)


def _scaled_box(
    box: tuple[float, float, float, float],
    width: int,
    height: int,
    native: BackendPage,
) -> BoundingBox:
    x, y, box_width, box_height = box
    return BoundingBox(
        x * native.width / width,
        y * native.height / height,
        box_width * native.width / width,
        box_height * native.height / height,
        CoordinateUnit.POINTS,
    )


def _ocr_evidence(
    result: OcrBackendResult, renderer_version: str,
) -> RecognitionEvidence:
    value = result.evidence
    config = _digest({
        "ocr": value.config_digest,
        "renderer": f"pypdfium2-{renderer_version}",
    })
    model = f"{value.package_version}:sha256:{value.model_digest}"
    return RecognitionEvidence("paddleocr-pdf", model, config)


def _aggregate_evidence(
    values: list[RecognitionEvidence],
) -> RecognitionEvidence:
    payload = [
        (item.engine, item.model_version, item.config_digest)
        for item in values
    ]
    return RecognitionEvidence(
        "paddle-pdf-secondary",
        "+".join(dict.fromkeys(item.model_version for item in values)),
        _digest(payload),
    )


def _digest(value: object) -> str:
    raw = json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


__all__ = [
    "EXPECTED_PDFIUM_VERSION",
    "PaddlePdfSecondaryBackend",
    "PdfiumPageRenderer",
    "RenderedPdfPage",
]
