"""Pure Docling JSON-to-intake-contract translation."""

from __future__ import annotations

import math
from typing import Any

from scripts.docling_content_order import ordered_content
from scripts.docling_layout_policy import complex_layout
from scripts.docling_picture_parser import picture_classification
from scripts.intake_extraction_contracts import (
    BlockKind,
    BoundingBox,
    CoordinateUnit,
    ExtractedTable,
    RecognitionEvidence,
    TableCell,
)
from scripts.pdf_document_extractor import (
    BackendBlock,
    BackendPage,
    PdfExtractionError,
)


def _corrupt(message: str) -> PdfExtractionError:
    return PdfExtractionError("PDF_CORRUPT", message)


def _number(value: object, default: float) -> float:
    if (
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not math.isfinite(value)
    ):
        return default
    return float(value)


def _required_number(data: dict[str, Any], key: str) -> float:
    value = _number(data.get(key), math.nan)
    if not math.isfinite(value):
        raise _corrupt("invalid PDF coordinate")
    return value


def _provenance(item: dict[str, Any]) -> dict[str, Any]:
    values = item.get("prov") or ()
    return values[0] if values and isinstance(values[0], dict) else {}


def _page_number(item: dict[str, Any], fallback: int = 1) -> int:
    value = _provenance(item).get("page_no", fallback)
    valid = isinstance(value, int) and not isinstance(value, bool)
    return value if valid else fallback


def _physical_page(item: dict[str, Any], forced_page: int | None) -> int:
    number = _page_number(item, 0)
    if number < 1 or (
        forced_page is not None and number != forced_page
    ):
        raise _corrupt("Docling physical page does not match request")
    return number


def _confidence(item: dict[str, Any], ocr: bool) -> float:
    provenance = _provenance(item)
    values = (
        item.get("confidence"),
        item.get("score"),
        provenance.get("confidence"),
        provenance.get("score"),
    )
    for value in values:
        number = _number(value, -1)
        if 0 <= number <= 1:
            return number
    return 0.0 if ocr else 1.0


def _box(raw: object, width: float, height: float) -> BoundingBox:
    if not isinstance(raw, dict):
        raise _corrupt("missing PDF coordinates")
    data = raw
    if {"l", "t", "r", "b"}.issubset(data):
        left = _required_number(data, "l")
        right = _required_number(data, "r")
        top = _required_number(data, "t")
        bottom = _required_number(data, "b")
        x = min(left, right)
        box_width = abs(right - left)
        box_height = abs(bottom - top)
        origin = str(data.get("coord_origin", "")).upper()
        y = height - max(top, bottom) if "BOTTOM" in origin else min(
            top, bottom
        )
    elif {"x", "y", "width", "height"}.issubset(data):
        x = _required_number(data, "x")
        y = _required_number(data, "y")
        box_width = _required_number(data, "width")
        box_height = _required_number(data, "height")
    else:
        raise _corrupt("invalid PDF coordinates")
    outside = (
        x < 0
        or y < 0
        or box_width <= 0
        or box_height <= 0
        or x + box_width > width
        or y + box_height > height
    )
    if outside:
        raise _corrupt("PDF coordinates exceed physical page")
    return BoundingBox(x, y, box_width, box_height, CoordinateUnit.POINTS)


def _table(
    item: dict[str, Any],
    box: BoundingBox,
    name: str,
    page_width: float,
    page_height: float,
) -> tuple[ExtractedTable | None, str]:
    data = item.get("data") if isinstance(item.get("data"), dict) else {}
    rows = max(int(data.get("num_rows") or 1), 1)
    columns = max(int(data.get("num_cols") or 1), 1)
    raw_cells = data.get("table_cells") or ()
    text = "\n".join(
        str(raw.get("text") or "")
        for raw in raw_cells
        if isinstance(raw, dict)
    ) or str(item.get("text") or "")
    if not raw_cells or any(
        not isinstance(raw, dict)
        or not isinstance(raw.get("bbox"), dict)
        for raw in raw_cells
    ):
        return None, text
    cells: list[TableCell] = []
    for raw in raw_cells:
        row = max(int(raw.get("start_row_offset_idx") or 0), 0)
        column = max(int(raw.get("start_col_offset_idx") or 0), 0)
        if row >= rows or column >= columns:
            raise _corrupt("invalid table cell")
        row_end = max(
            int(raw.get("end_row_offset_idx") or row + 1), row + 1
        )
        column_end = max(
            int(raw.get("end_col_offset_idx") or column + 1), column + 1
        )
        row_span = min(row_end - row, rows - row)
        column_span = min(column_end - column, columns - column)
        coordinates = _box(raw["bbox"], page_width, page_height)
        cells.append(
            TableCell(
                row,
                column,
                str(raw.get("text") or ""),
                coordinates,
                row_span,
                column_span,
            )
        )
    return ExtractedTable(name, box, rows, columns, tuple(cells)), text


def _dimensions(
    raw_pages: object, forced_page: int | None
) -> dict[int, tuple[float, float]]:
    if not isinstance(raw_pages, dict):
        raise _corrupt("invalid Docling pages")
    if forced_page is not None and len(raw_pages) != 1:
        raise _corrupt("forced OCR must contain one physical page")
    result: dict[int, tuple[float, float]] = {}
    for key, value in raw_pages.items():
        if not isinstance(value, dict):
            raise _corrupt("invalid Docling page")
        if isinstance(key, bool) or not isinstance(key, (int, str)):
            raise _corrupt("invalid Docling page number")
        number = int(key)
        if str(number) != str(key):
            raise _corrupt("invalid Docling page number")
        if number < 1 or (
            forced_page is not None and number != forced_page
        ):
            raise _corrupt("Docling page range does not match request")
        if number in result:
            raise _corrupt("duplicate PDF page")
        size = value.get("size") or value
        if not isinstance(size, dict):
            raise _corrupt("invalid PDF page size")
        width = _required_number(size, "width")
        height = _required_number(size, "height")
        if width <= 0 or height <= 0:
            raise _corrupt("invalid PDF page size")
        result[number] = (width, height)
    return result


def _content_blocks(
    payload: dict[str, Any],
    dimensions: dict[int, tuple[float, float]],
    ocr: bool,
    forced_page: int | None,
    picture_evidence: RecognitionEvidence | None,
) -> dict[int, list[BackendBlock]]:
    blocks: dict[int, list[BackendBlock]] = {}
    for kind, item in ordered_content(payload):
        number = _physical_page(item, forced_page)
        if number not in dimensions:
            raise _corrupt("content references a missing PDF page")
        width, height = dimensions[number]
        box = _box(_provenance(item).get("bbox"), width, height)
        tables: tuple[ExtractedTable, ...] = ()
        classification = None
        text = str(item.get("text") or "")
        confidence = _confidence(item, ocr)
        if kind is BlockKind.TABLE:
            ordinal = len(blocks.get(number, ())) + 1
            table, text = _table(
                item,
                box,
                f"page-{number}-table-{ordinal}",
                width,
                height,
            )
            if table is None:
                kind = BlockKind.TEXT
                confidence = 0.0
            else:
                tables = (table,)
        if kind is BlockKind.IMAGE:
            classification = picture_classification(
                item,
                picture_evidence,
            )
            if classification is None:
                continue
            text = ""
            confidence = classification.confidence
        block = BackendBlock(
            kind,
            box,
            text,
            confidence,
            tables,
            classification,
        )
        blocks.setdefault(number, []).append(block)
    return blocks


def _picture_areas(
    payload: dict[str, Any],
    dimensions: dict[int, tuple[float, float]],
    forced_page: int | None,
) -> dict[int, float]:
    areas: dict[int, float] = {}
    for item in payload.get("pictures") or ():
        if not isinstance(item, dict):
            continue
        number = _physical_page(item, forced_page)
        if number not in dimensions:
            raise _corrupt("picture references a missing PDF page")
        width, height = dimensions[number]
        box = _box(_provenance(item).get("bbox"), width, height)
        areas[number] = max(areas.get(number, 0), box.width * box.height)
    return areas


def _parse_docling_payload(
    payload: dict[str, Any],
    *,
    ocr: bool,
    forced_page: int | None = None,
    picture_evidence: RecognitionEvidence | None = None,
) -> tuple[BackendPage, ...]:
    dimensions = _dimensions(payload.get("pages") or {}, forced_page)
    blocks = _content_blocks(
        payload,
        dimensions,
        ocr,
        forced_page,
        picture_evidence,
    )
    pictures = _picture_areas(payload, dimensions, forced_page)
    return tuple(
        BackendPage(
            number,
            dimensions[number][0],
            dimensions[number][1],
            CoordinateUnit.POINTS,
            tuple(blocks.get(number, ())),
            not ocr
            and pictures.get(number, 0)
            / (dimensions[number][0] * dimensions[number][1])
            >= 0.20,
            pictures.get(number, 0) > 0,
            complex_layout(blocks.get(number, [])),
        )
        for number in sorted(dimensions)
    )


def parse_docling_payload(
    payload: dict[str, Any],
    *,
    ocr: bool,
    forced_page: int | None = None,
    picture_evidence: RecognitionEvidence | None = None,
) -> tuple[BackendPage, ...]:
    """Translate exported Docling JSON or reject malformed output."""
    try:
        return _parse_docling_payload(
            payload,
            ocr=ocr,
            forced_page=forced_page,
            picture_evidence=picture_evidence,
        )
    except PdfExtractionError:
        raise
    except (AttributeError, KeyError, TypeError, ValueError) as error:
        raise _corrupt("invalid Docling extraction payload") from error
