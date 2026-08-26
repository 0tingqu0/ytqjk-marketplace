from __future__ import annotations

import re
import unicodedata
from dataclasses import dataclass

from scripts.image_ocr_backend import OcrBackendResult, OcrTextBlock
from scripts.intake_extraction_contracts import (
    BlockKind,
    BoundingBox,
    CoordinateUnit,
    ExtractedBlock,
    ExtractionMode,
    ImageClassification,
    RecognitionEvidence,
)


LocatedBlocks = tuple[tuple[str, BoundingBox, float], ...]


@dataclass(frozen=True)
class ImageFeatures:
    filename: str
    block_count: int
    character_count: int
    row_count: int
    column_count: int
    text_sample: str


def located_blocks(backend: OcrBackendResult) -> LocatedBlocks:
    located: list[tuple[str, BoundingBox, float]] = []
    for raw in backend.blocks:
        if type(raw) is not OcrTextBlock:
            raise TypeError("invalid OCR block type")
        text = _normalized_text(raw.text)
        if not text:
            raise ValueError("OCR text is empty after normalization")
        box = _box(raw, backend.width, backend.height)
        located.append((text, box, raw.confidence))
    located.sort(key=lambda item: (
        item[1].y,
        item[1].x,
        item[0],
        item[2],
    ))
    return tuple(located)


def image_features(
    filename: str,
    backend: OcrBackendResult,
    located: LocatedBlocks,
) -> ImageFeatures:
    rows = _axis_groups(
        [box.y + box.height / 2 for _, box, _ in located],
        max(8, backend.height * 0.03),
    )
    columns = _axis_groups(
        [box.x + box.width / 2 for _, box, _ in located],
        max(8, backend.width * 0.03),
    )
    sample = " ".join(text for text, _, _ in located)[:512]
    characters = sum(len(text) for text, _, _ in located)
    return ImageFeatures(
        filename,
        len(located),
        characters,
        rows,
        columns,
        sample,
    )


def contract_blocks(
    backend: OcrBackendResult,
    located: LocatedBlocks,
    evidence: RecognitionEvidence,
    classification: ImageClassification,
) -> tuple[ExtractedBlock, ...]:
    image_box = BoundingBox(
        0,
        0,
        backend.width,
        backend.height,
        CoordinateUnit.PIXELS,
    )
    image = ExtractedBlock(
        "image-1",
        1,
        1,
        BlockKind.IMAGE,
        image_box,
        classification.summary,
        ExtractionMode.OCR,
        classification.confidence,
        classification.evidence,
        image_classification=classification,
    )
    text = tuple(
        ExtractedBlock(
            f"text-{ordinal}",
            1,
            ordinal + 1,
            BlockKind.TEXT,
            box,
            value,
            ExtractionMode.OCR,
            confidence,
            evidence,
        )
        for ordinal, (value, box, confidence) in enumerate(located, 1)
    )
    return (image, *text)


def _normalized_text(value: str) -> str:
    normalized = unicodedata.normalize("NFKC", value)
    cleaned = [
        " " if char.isspace() else char
        for char in normalized
        if char.isspace() or unicodedata.category(char) != "Cc"
    ]
    return re.sub(r" +", " ", "".join(cleaned)).strip()


def _box(raw: OcrTextBlock, width: int, height: int) -> BoundingBox:
    xs = [point.x for point in raw.quad]
    ys = [point.y for point in raw.quad]
    left, right = min(xs), max(xs)
    top, bottom = min(ys), max(ys)
    if right > width or bottom > height:
        raise ValueError("OCR coordinates exceed image dimensions")
    return BoundingBox(
        left,
        top,
        right - left,
        bottom - top,
        CoordinateUnit.PIXELS,
    )


def _axis_groups(values: list[float], tolerance: float) -> int:
    groups: list[float] = []
    for value in sorted(values):
        if not groups or value - groups[-1] > tolerance:
            groups.append(value)
    return len(groups)
