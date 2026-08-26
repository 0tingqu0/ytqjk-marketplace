from __future__ import annotations

import json
import sys
from dataclasses import replace
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.intake_extraction_contracts import (  # noqa: E402
    BlockKind,
    BoundingBox,
    CONFIDENCE_THRESHOLD,
    CoordinateUnit,
    ExtractedBlock,
    ExtractedPage,
    ExtractedTable,
    ExtractionMode,
    ExtractionResult,
    ImageClassification,
    LOW_CONFIDENCE_REASON,
    QualityStatus,
    RecognitionEvidence,
    RecognitionRound,
    TableCell,
    canonical_digest,
    canonical_json,
)


DIGEST_A = "a" * 64
DIGEST_B = "b" * 64


def _evidence(engine: str = "vision") -> RecognitionEvidence:
    return RecognitionEvidence(engine, "v2", DIGEST_A)


def _table(box: BoundingBox) -> ExtractedTable:
    cell_box = BoundingBox(
        box.x + 1, box.y + 1, 5, 3, box.unit
    )
    cell = TableCell(0, 0, "value", cell_box)
    return ExtractedTable("table-1", box, 1, 1, (cell,))


def _result(*, confidence: float = 0.98) -> ExtractionResult:
    unit = CoordinateUnit.PIXELS
    table_box = BoundingBox(2, 2, 20, 10, unit)
    table_block_box = BoundingBox(1, 1, 30, 20, unit)
    image_box = BoundingBox(40, 1, 20, 20, unit)
    classification = ImageClassification(
        "robot", ("indoor", "inspection"), "inspection robot", 0.98,
        _evidence("classifier"),
    )
    table_block = ExtractedBlock(
        "block-1", 1, 1, BlockKind.TABLE, table_block_box,
        "value", ExtractionMode.NATIVE_TEXT, 0.99, _evidence("pdf"),
        (_table(table_box),),
    )
    image_block = ExtractedBlock(
        "block-2", 1, 2, BlockKind.IMAGE, image_box,
        "detected", ExtractionMode.OCR, confidence, _evidence("ocr"),
        image_classification=classification,
    )
    page = ExtractedPage(
        1, 100, 100, unit, ExtractionMode.MIXED,
        (table_block, image_block),
    )
    rounds = (
        RecognitionRound(1, ExtractionMode.NATIVE_TEXT, _evidence("pdf"), 4),
        RecognitionRound(2, ExtractionMode.OCR, _evidence("ocr"), 12),
    )
    return ExtractionResult(
        DIGEST_A, DIGEST_B, ExtractionMode.MIXED, rounds, (page,),
        QualityStatus.ACCEPTABLE, (), 16,
    )


def test_contract_covers_structured_extraction_and_classification() -> None:
    result = _result()
    table_block, block = result.pages[0].blocks
    classification = block.image_classification
    assert classification is not None
    assert classification.category == "robot"
    assert classification.tags == ("indoor", "inspection")
    assert classification.evidence.model_version == "v2"
    assert classification.evidence.config_digest == DIGEST_A
    assert table_block.kind is BlockKind.TABLE
    assert block.kind is BlockKind.IMAGE
    assert block.confidence == 0.98
    assert block.evidence.engine == "ocr"
    assert block.coordinates.unit is CoordinateUnit.PIXELS
    assert result.pages[0].mode is ExtractionMode.MIXED
    assert [item.mode for item in result.rounds] == [
        ExtractionMode.NATIVE_TEXT,
        ExtractionMode.OCR,
    ]


def test_canonical_json_and_digest_are_stable() -> None:
    first = _result()
    second = _result()
    payload = canonical_json(first)
    assert payload == canonical_json(second)
    assert json.loads(payload)["quality"] == "ACCEPTABLE"
    assert canonical_digest(first) == canonical_digest(second)


@pytest.mark.parametrize(
    ("change", "message"),
    (
        ({"source_digest": "A" * 64}, "lowercase SHA-256"),
        ({"mode": "OCR"}, "invalid result mode"),
        ({"elapsed_ms": -1}, "elapsed_ms must be at least 0"),
        ({"review_reasons": ("manual",)}, "acceptable result"),
        ({"rounds": ()}, "needs rounds and pages"),
    ),
)
def test_result_rejects_invalid_values(
    change: dict[str, object], message: str
) -> None:
    with pytest.raises(ValueError, match=message):
        replace(_result(), **change)


@pytest.mark.parametrize(
    "quality", (QualityStatus.REVIEW_REQUIRED, QualityStatus.FAILED)
)
def test_non_acceptable_quality_requires_review_reason(
    quality: QualityStatus,
) -> None:
    with pytest.raises(ValueError, match="review reason is required"):
        replace(_result(), quality=quality)


@pytest.mark.parametrize(
    "unsafe",
    (
        r"C:\Users\operator\source.png",
        r"\\server\private\source.png",
        "/home/operator/source.png",
    ),
)
def test_body_text_may_contain_absolute_path(unsafe: str) -> None:
    block = _result().pages[0].blocks[0]
    assert replace(block, text=unsafe).text == unsafe


def test_low_confidence_requires_review_status_and_reason() -> None:
    result = _result()
    blocks = result.pages[0].blocks
    low_block = replace(blocks[1], confidence=CONFIDENCE_THRESHOLD - 0.01)
    low_page = replace(result.pages[0], blocks=(blocks[0], low_block))
    with pytest.raises(ValueError, match="forbids ACCEPTABLE"):
        replace(result, pages=(low_page,))
    with pytest.raises(ValueError, match="missing low confidence reason"):
        replace(
            result,
            pages=(low_page,),
            quality=QualityStatus.REVIEW_REQUIRED,
            review_reasons=("MANUAL",),
        )
    reviewed = replace(
        result,
        pages=(low_page,),
        quality=QualityStatus.REVIEW_REQUIRED,
        review_reasons=(LOW_CONFIDENCE_REASON,),
    )
    assert reviewed.quality is QualityStatus.REVIEW_REQUIRED
    failed = replace(
        result,
        pages=(low_page,),
        quality=QualityStatus.FAILED,
        review_reasons=("EXTRACTION_FAILED",),
    )
    assert failed.quality is QualityStatus.FAILED
    assert _result(confidence=CONFIDENCE_THRESHOLD).quality is (
        QualityStatus.ACCEPTABLE
    )


def test_block_mode_kind_and_evidence_are_strict() -> None:
    result = _result()
    table_block = result.pages[0].blocks[0]
    with pytest.raises(ValueError, match="NATIVE_TEXT or OCR"):
        replace(table_block, mode=ExtractionMode.MIXED)
    with pytest.raises(ValueError, match="TEXT block content"):
        replace(table_block, kind=BlockKind.TEXT)
    with pytest.raises(ValueError, match="lowercase SHA-256"):
        RecognitionEvidence("ocr", "v2", "invalid")
    with pytest.raises(ValueError, match="NATIVE_TEXT or OCR"):
        replace(result.rounds[0], mode=ExtractionMode.MIXED)


def test_table_cell_block_and_page_coordinates_are_contained() -> None:
    result = _result()
    page = result.pages[0]
    table_block = page.blocks[0]
    unit = CoordinateUnit.PIXELS
    outside_table = _table(BoundingBox(25, 2, 20, 10, unit))
    with pytest.raises(ValueError, match="table must be within block"):
        replace(table_block, tables=(outside_table,))
    table = table_block.tables[0]
    outside_cell = TableCell(
        0, 0, "outside", BoundingBox(25, 3, 3, 3, unit)
    )
    with pytest.raises(ValueError, match="cell must be within table"):
        replace(table, cells=(outside_cell,))
    outside_block = replace(
        page.blocks[1], coordinates=BoundingBox(90, 1, 20, 20, unit)
    )
    with pytest.raises(ValueError, match="block must be within page"):
        replace(page, blocks=(table_block, outside_block))
    wrong_unit = _table(BoundingBox(2, 2, 20, 10, CoordinateUnit.POINTS))
    with pytest.raises(ValueError, match="table must be within block"):
        replace(table_block, tables=(wrong_unit,))


def test_table_overlapping_cells_are_rejected() -> None:
    unit = CoordinateUnit.PIXELS
    table_box = BoundingBox(0, 0, 20, 20, unit)
    cell_box = BoundingBox(1, 1, 5, 5, unit)
    cells = (TableCell(0, 0, "a", cell_box), TableCell(0, 0, "b", cell_box))
    with pytest.raises(ValueError, match="overlapping table cell"):
        ExtractedTable("table", table_box, 1, 1, cells)


def test_canonical_rejects_other_objects_and_forged_contracts() -> None:
    with pytest.raises(TypeError, match="requires an extraction contract"):
        canonical_json({"quality": "ACCEPTABLE"})
    result = _result()
    object.__setattr__(result.pages[0].blocks[1], "confidence", -1)
    with pytest.raises(ValueError, match="invalid extraction contract"):
        canonical_json(result)
    evidence = _evidence()
    object.__setattr__(evidence, "config_digest", "forged")
    with pytest.raises(ValueError, match="invalid extraction contract"):
        canonical_digest(evidence)


def test_canonical_rejects_nested_subclasses_and_extra_fields() -> None:
    class NestedBox(BoundingBox):
        pass

    result = _result()
    block = replace(
        result.pages[0].blocks[0],
        coordinates=NestedBox(1, 1, 30, 20, CoordinateUnit.PIXELS),
    )
    page = replace(result.pages[0], blocks=(block, result.pages[0].blocks[1]))
    with pytest.raises(ValueError, match="invalid extraction contract"):
        canonical_json(replace(result, pages=(page,)))
    result = _result()
    object.__setattr__(result.pages[0], "unexpected", "value")
    with pytest.raises(ValueError, match="invalid extraction contract"):
        canonical_json(result)


def test_empty_physical_page_is_valid_without_fabricated_block() -> None:
    result = _result()
    blank = ExtractedPage(
        2, 100, 100, CoordinateUnit.PIXELS, ExtractionMode.OCR, (),
    )
    combined = replace(result, pages=(result.pages[0], blank))
    assert combined.pages[1].blocks == ()


def test_canonical_numbers_normalize_int_and_float() -> None:
    unit = CoordinateUnit.PIXELS
    integer = BoundingBox(1, 2, 3, 4, unit)
    floating = BoundingBox(1.0, 2.0, 3.0, 4.0, unit)
    assert canonical_json(integer) == canonical_json(floating)
    assert canonical_digest(integer) == canonical_digest(floating)


@pytest.mark.parametrize(
    "factory",
    (
        lambda huge: BoundingBox(huge, 0, 1, 1, CoordinateUnit.PIXELS),
        lambda huge: RecognitionRound(
            huge, ExtractionMode.OCR, _evidence(), 1
        ),
        lambda huge: TableCell(
            huge, 0, "x", BoundingBox(0, 0, 1, 1, CoordinateUnit.PIXELS)
        ),
        lambda huge: replace(_result(), elapsed_ms=huge),
    ),
)
def test_oversized_integers_always_raise_value_error(factory) -> None:
    with pytest.raises(ValueError):
        factory(10**1000)
