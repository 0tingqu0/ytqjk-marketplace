"""Deterministic, locator-preserving chunks from extraction contracts."""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass

from .intake_extraction_contracts import (
    BoundingBox,
    ExtractedPage,
    ExtractedBlock,
    ExtractedTable,
    ExtractionResult,
    ImageClassification,
    RecognitionEvidence,
    RecognitionRound,
    TableCell,
)


MAX_CHUNK_CHARACTERS = 2000


@dataclass(frozen=True)
class ChunkLocator:
    """Physical source location required to trace a chunk back to evidence."""

    page_number: int
    block_id: str
    bounding_box: BoundingBox
    table_ids: tuple[str, ...]


@dataclass(frozen=True)
class StructuredChunk:
    """One deterministic, storage-agnostic candidate knowledge chunk."""

    id: str
    digest: str
    ordinal: int
    text: str
    locator: ChunkLocator


def _canonical(value: object) -> str:
    return json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True,
    )


def _digest(value: object) -> str:
    return hashlib.sha256(_canonical(value).encode("utf-8")).hexdigest()


def _box_value(box: BoundingBox) -> dict[str, object]:
    return {
        "height": box.height,
        "unit": box.unit.value,
        "width": box.width,
        "x": box.x,
        "y": box.y,
    }


def _table_text(table: ExtractedTable) -> str:
    cells = sorted(table.cells, key=lambda cell: (cell.row, cell.column))
    rows: dict[int, list[str]] = {}
    for cell in cells:
        rows.setdefault(cell.row, []).append(cell.text.strip())
    return "\n".join(" | ".join(rows[row]) for row in sorted(rows)).strip()


def _block_text(block: ExtractedBlock) -> str:
    text = block.text.strip()
    table_text = "\n\n".join(
        part for part in (_table_text(table) for table in block.tables) if part
    )
    parts = [text] if text else []
    if table_text and table_text not in text:
        parts.append(table_text)
    image = block.image_classification
    if image is not None:
        if image.summary not in text:
            parts.append(f"图片内容：{image.summary}")
        parts.append(f"图片分类：{image.category}")
        parts.append(f"图片标签：{'、'.join(image.tags)}")
    return "\n\n".join(parts)


def _segments(text: str) -> tuple[str, ...]:
    """Prefer paragraph boundaries; only split a single overlong paragraph."""
    remaining = text.strip()
    chunks: list[str] = []
    while remaining:
        if len(remaining) <= MAX_CHUNK_CHARACTERS:
            chunks.append(remaining)
            break
        window = remaining[: MAX_CHUNK_CHARACTERS + 1]
        breaks = [window.rfind(mark) for mark in ("\n\n", "\n", "。", ". ")]
        split_at = max(breaks)
        if split_at <= 0:
            split_at = MAX_CHUNK_CHARACTERS
        else:
            split_at += 1
        piece = remaining[:split_at].strip()
        if not piece:
            split_at = MAX_CHUNK_CHARACTERS
            piece = remaining[:split_at]
        chunks.append(piece)
        remaining = remaining[split_at:].lstrip()
    return tuple(chunks)


def _chunk(
    ordinal: int,
    text: str,
    block: ExtractedBlock,
) -> StructuredChunk:
    locator = ChunkLocator(
        page_number=block.page_number,
        block_id=block.block_id,
        bounding_box=block.coordinates,
        table_ids=tuple(table.table_id for table in block.tables),
    )
    payload = {
        "locator": {
            "block_id": locator.block_id,
            "bounding_box": _box_value(locator.bounding_box),
            "page_number": locator.page_number,
            "table_ids": locator.table_ids,
        },
        "text": text,
    }
    digest = _digest(payload)
    return StructuredChunk(
        id=hashlib.sha256(f"structured-chunk-v1:{digest}".encode()).hexdigest(),
        digest=digest,
        ordinal=ordinal,
        text=text,
        locator=locator,
    )


def validate_structured_result(result: object) -> ExtractionResult:
    """Reject nested subclasses and repeated table cells before planning."""
    if type(result) is not ExtractionResult:
        raise ValueError("result type is invalid")
    for round_item in result.rounds:
        if type(round_item) is not RecognitionRound:
            raise ValueError("round type is invalid")
        if type(round_item.evidence) is not RecognitionEvidence:
            raise ValueError("round evidence type is invalid")
    for page in result.pages:
        if type(page) is not ExtractedPage:
            raise ValueError("page type is invalid")
        for block in page.blocks:
            if type(block) is not ExtractedBlock:
                raise ValueError("block type is invalid")
            if type(block.coordinates) is not BoundingBox:
                raise ValueError("block box type is invalid")
            if type(block.evidence) is not RecognitionEvidence:
                raise ValueError("block evidence type is invalid")
            image = block.image_classification
            if image is not None and type(image) is not ImageClassification:
                raise ValueError("image classification type is invalid")
            for table in block.tables:
                _validate_table(table)
    return result


def _validate_table(table: object) -> None:
    if type(table) is not ExtractedTable:
        raise ValueError("table type is invalid")
    if type(table.coordinates) is not BoundingBox:
        raise ValueError("table box type is invalid")
    seen: set[tuple[int, int]] = set()
    for cell in table.cells:
        if type(cell) is not TableCell:
            raise ValueError("cell type is invalid")
        if type(cell.coordinates) is not BoundingBox:
            raise ValueError("cell box type is invalid")
        for row in range(cell.row, cell.row + cell.row_span):
            for column in range(cell.column, cell.column + cell.column_span):
                key = (row, column)
                if key in seen:
                    raise ValueError("overlapping table cell")
                seen.add(key)


def build_structured_chunks(
    result: ExtractionResult,
) -> tuple[StructuredChunk, ...]:
    """Build ordered chunks without fabricating text for blank source blocks."""
    chunks: list[StructuredChunk] = []
    for page in result.pages:
        for block in page.blocks:
            text = re.sub(r"\r\n?", "\n", _block_text(block)).strip()
            if not text:
                continue
            for segment in _segments(text):
                chunks.append(_chunk(len(chunks) + 1, segment, block))
    return tuple(chunks)
