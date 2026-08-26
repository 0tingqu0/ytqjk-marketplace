from __future__ import annotations

import math
import re
from dataclasses import dataclass
from enum import Enum


CONFIDENCE_THRESHOLD, LOW_CONFIDENCE_REASON = 0.88, "LOW_CONFIDENCE"
_MAX_INT = 2**63 - 1
_SHA256 = re.compile(r"^[0-9a-f]{64}$")


class ExtractionMode(str, Enum):
    NATIVE_TEXT, OCR, MIXED = "NATIVE_TEXT", "OCR", "MIXED"


class QualityStatus(str, Enum):
    ACCEPTABLE, REVIEW_REQUIRED, FAILED = (
        "ACCEPTABLE", "REVIEW_REQUIRED", "FAILED")


class BlockKind(str, Enum):
    TEXT, TABLE, IMAGE = "TEXT", "TABLE", "IMAGE"


class CoordinateUnit(str, Enum):
    PIXELS, POINTS = "PIXELS", "POINTS"


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def _text(name: str, value: object, *, empty: bool = False) -> None:
    valid = isinstance(value, str) and (empty or bool(value.strip()))
    _require(valid, f"{name} must be non-empty text")


def _digest(name: str, value: object) -> None:
    valid = isinstance(value, str) and _SHA256.fullmatch(value) is not None
    _require(valid, f"{name} must be a lowercase SHA-256 digest")


def _integer(name: str, value: object, minimum: int) -> None:
    valid = isinstance(value, int) and not isinstance(value, bool)
    _require(valid, f"{name} must be an integer")
    _require(value <= _MAX_INT, f"{name} is too large")
    _require(value >= minimum, f"{name} must be at least {minimum}")


def _number(name: str, value: object, minimum: float) -> None:
    valid = isinstance(value, (int, float)) and not isinstance(value, bool)
    _require(valid, f"{name} must be a finite number")
    _require(not isinstance(value, int) or abs(value) <= _MAX_INT,
             f"large {name}")
    finite = not isinstance(value, float) or math.isfinite(value)
    _require(finite and value >= minimum, f"invalid {name}")


def _tuple(name: str, value: object, item_type: type[object]) -> None:
    valid = isinstance(value, tuple) and all(
        isinstance(item, item_type) for item in value
    )
    _require(valid, f"{name} contains an invalid item")


def _confidence(value: object) -> None:
    _number("confidence", value, 0)
    _require(value <= 1, "confidence must not exceed 1")


def _recognition_mode(value: object) -> None:
    valid = type(value) is ExtractionMode and value is not ExtractionMode.MIXED
    _require(valid, "recognition mode must be NATIVE_TEXT or OCR")


@dataclass(frozen=True)
class BoundingBox:
    x: float
    y: float
    width: float
    height: float
    unit: CoordinateUnit

    def __post_init__(self) -> None:
        for name in ("x", "y", "width", "height"):
            _number(name, getattr(self, name), 0)
        _require(self.width > 0 and self.height > 0, "invalid box size")
        _require(isinstance(self.unit, CoordinateUnit), "invalid box unit")


def _contains(outer: BoundingBox, inner: BoundingBox) -> bool:
    if outer.unit is not inner.unit:
        return False
    return (
        inner.x >= outer.x
        and inner.y >= outer.y
        and inner.x + inner.width <= outer.x + outer.width
        and inner.y + inner.height <= outer.y + outer.height
    )


@dataclass(frozen=True)
class RecognitionEvidence:
    engine: str
    model_version: str
    config_digest: str

    def __post_init__(self) -> None:
        _text("engine", self.engine)
        _text("model_version", self.model_version)
        _digest("config_digest", self.config_digest)


@dataclass(frozen=True)
class ImageClassification:
    category: str
    tags: tuple[str, ...]
    summary: str
    confidence: float
    evidence: RecognitionEvidence
    elapsed_ms: int = 0

    def __post_init__(self) -> None:
        _text("category", self.category)
        _tuple("tags", self.tags, str)
        unique = bool(self.tags) and len(set(self.tags)) == len(self.tags)
        _require(unique, "tags must be non-empty and unique")
        for tag in self.tags:
            _text("tag", tag)
        _text("summary", self.summary)
        _confidence(self.confidence)
        valid = isinstance(self.evidence, RecognitionEvidence)
        _require(valid, "invalid classification evidence")
        _integer("elapsed_ms", self.elapsed_ms, 0)


@dataclass(frozen=True)
class RecognitionRound:
    ordinal: int
    mode: ExtractionMode
    evidence: RecognitionEvidence
    elapsed_ms: int

    def __post_init__(self) -> None:
        _integer("ordinal", self.ordinal, 1)
        _recognition_mode(self.mode)
        valid = isinstance(self.evidence, RecognitionEvidence)
        _require(valid, "invalid recognition evidence")
        _integer("elapsed_ms", self.elapsed_ms, 0)


@dataclass(frozen=True)
class TableCell:
    row: int
    column: int
    text: str
    coordinates: BoundingBox
    row_span: int = 1
    column_span: int = 1

    def __post_init__(self) -> None:
        _integer("row", self.row, 0)
        _integer("column", self.column, 0)
        _text("text", self.text, empty=True)
        _require(isinstance(self.coordinates, BoundingBox), "invalid cell box")
        _integer("row_span", self.row_span, 1)
        _integer("column_span", self.column_span, 1)


@dataclass(frozen=True)
class ExtractedTable:
    table_id: str
    coordinates: BoundingBox
    row_count: int
    column_count: int
    cells: tuple[TableCell, ...]

    def __post_init__(self) -> None:
        _text("table_id", self.table_id)
        _require(isinstance(self.coordinates, BoundingBox), "invalid table box")
        _integer("row_count", self.row_count, 1)
        _integer("column_count", self.column_count, 1)
        _tuple("cells", self.cells, TableCell)
        occupied: set[tuple[int, int]] = set()
        for cell in self.cells:
            inside = _contains(self.coordinates, cell.coordinates)
            _require(inside, "cell must be within table")
            _require(cell.row + cell.row_span <= self.row_count,
                     "cell exceeds table rows")
            column_end = cell.column + cell.column_span
            _require(column_end <= self.column_count,
                     "cell exceeds table columns")
            for row in range(cell.row, cell.row + cell.row_span):
                for column in range(cell.column, column_end):
                    position = (row, column)
                    _require(position not in occupied, "overlapping table cell")
                    occupied.add(position)


@dataclass(frozen=True)
class ExtractedBlock:
    block_id: str
    page_number: int
    ordinal: int
    kind: BlockKind
    coordinates: BoundingBox
    text: str
    mode: ExtractionMode
    confidence: float
    evidence: RecognitionEvidence
    tables: tuple[ExtractedTable, ...] = ()
    image_classification: ImageClassification | None = None

    def __post_init__(self) -> None:
        _text("block_id", self.block_id)
        _integer("page_number", self.page_number, 1)
        _integer("ordinal", self.ordinal, 1)
        _require(isinstance(self.kind, BlockKind), "block kind is invalid")
        _require(isinstance(self.coordinates, BoundingBox), "invalid block box")
        _text("text", self.text, empty=True)
        _recognition_mode(self.mode)
        _confidence(self.confidence)
        valid_evidence = isinstance(self.evidence, RecognitionEvidence)
        _require(valid_evidence, "invalid block evidence")
        _tuple("tables", self.tables, ExtractedTable)
        invalid_table = any(
            not _contains(self.coordinates, table.coordinates)
            for table in self.tables
        )
        _require(not invalid_table, "table must be within block")
        self._validate_kind()

    def _validate_kind(self) -> None:
        image = self.image_classification
        valid_image = image is None or isinstance(image, ImageClassification)
        _require(valid_image, "image_classification is invalid")
        valid = {
            BlockKind.TEXT: not self.tables and image is None,
            BlockKind.TABLE: bool(self.tables) and image is None,
            BlockKind.IMAGE: not self.tables and image is not None,
        }[self.kind]
        _require(valid, f"{self.kind.value} block content is invalid")


def _combined_mode(items: tuple[object, ...]) -> ExtractionMode:
    modes = {item.mode for item in items}
    return ExtractionMode.MIXED if len(modes) > 1 else next(iter(modes))


@dataclass(frozen=True)
class ExtractedPage:
    number: int
    width: float
    height: float
    coordinate_unit: CoordinateUnit
    mode: ExtractionMode
    blocks: tuple[ExtractedBlock, ...]

    def __post_init__(self) -> None:
        _integer("number", self.number, 1)
        _number("width", self.width, 0)
        _number("height", self.height, 0)
        _require(self.width > 0 and self.height > 0, "invalid page size")
        valid_unit = isinstance(self.coordinate_unit, CoordinateUnit)
        _require(valid_unit, "invalid page unit")
        _require(isinstance(self.mode, ExtractionMode), "invalid page mode")
        _tuple("blocks", self.blocks, ExtractedBlock)
        if not self.blocks:
            return
        ordinals = [block.ordinal for block in self.blocks]
        expected = list(range(1, len(self.blocks) + 1))
        _require(ordinals == expected, "non-contiguous block ordinals")
        ids = {block.block_id for block in self.blocks}
        _require(len(ids) == len(self.blocks), "duplicate block_id")
        page_numbers = all(
            block.page_number == self.number for block in self.blocks
        )
        _require(page_numbers, "block has wrong page_number")
        page_box = BoundingBox(
            0, 0, self.width, self.height, self.coordinate_unit
        )
        inside = all(
            _contains(page_box, block.coordinates) for block in self.blocks
        )
        _require(inside, "block must be within page")
        _require(self.mode is _combined_mode(self.blocks),
                 "page mode does not match blocks")


@dataclass(frozen=True)
class ExtractionResult:
    source_digest: str
    config_digest: str
    mode: ExtractionMode
    rounds: tuple[RecognitionRound, ...]
    pages: tuple[ExtractedPage, ...]
    quality: QualityStatus
    review_reasons: tuple[str, ...]
    elapsed_ms: int

    def __post_init__(self) -> None:
        _digest("source_digest", self.source_digest)
        _digest("config_digest", self.config_digest)
        _require(isinstance(self.mode, ExtractionMode), "invalid result mode")
        _tuple("rounds", self.rounds, RecognitionRound)
        _tuple("pages", self.pages, ExtractedPage)
        _require(isinstance(self.quality, QualityStatus), "invalid quality")
        _tuple("review_reasons", self.review_reasons, str)
        for reason in self.review_reasons:
            _text("review_reason", reason)
        _integer("elapsed_ms", self.elapsed_ms, 0)
        self._validate_sequences()
        self._validate_quality()

    def _validate_sequences(self) -> None:
        complete = bool(self.rounds) and bool(self.pages)
        valid = self.quality is QualityStatus.FAILED or complete
        _require(valid, "non-failed result needs rounds and pages")
        ordinals = [item.ordinal for item in self.rounds]
        _require(ordinals == list(range(1, len(self.rounds) + 1)),
                 "non-contiguous recognition rounds")
        numbers = [page.number for page in self.pages]
        _require(numbers == list(range(1, len(self.pages) + 1)),
                 "non-contiguous page numbers")
        _require(len(set(self.review_reasons)) == len(self.review_reasons),
                 "duplicate review reason")
        if self.pages:
            _require(self.mode is _combined_mode(self.pages),
                     "result mode does not match pages")
        if self.rounds:
            _require(self.mode is _combined_mode(self.rounds),
                     "result mode does not match rounds")
        total = sum(item.elapsed_ms for item in self.rounds)
        _require(self.elapsed_ms >= total, "elapsed_ms below round total")

    def _validate_quality(self) -> None:
        blocks = [block for page in self.pages for block in page.blocks]
        confidence = [block.confidence for block in blocks]
        confidence += [block.image_classification.confidence
                       for block in blocks
                       if block.image_classification is not None]
        low = any(value < CONFIDENCE_THRESHOLD for value in confidence)
        if low:
            valid = self.quality is not QualityStatus.ACCEPTABLE
            _require(valid, "low confidence forbids ACCEPTABLE")
            if self.quality is QualityStatus.REVIEW_REQUIRED:
                valid = LOW_CONFIDENCE_REASON in self.review_reasons
                _require(valid, "missing low confidence reason")
        if self.quality is QualityStatus.ACCEPTABLE:
            _require(not self.review_reasons, "acceptable result has reasons")
        else:
            _require(bool(self.review_reasons), "review reason is required")

def canonical_json(value: object) -> str:
    from .intake_extraction_serialization import canonical_json as encode

    return encode(value)


def canonical_digest(value: object) -> str:
    from .intake_extraction_serialization import canonical_digest as digest

    return digest(value)
