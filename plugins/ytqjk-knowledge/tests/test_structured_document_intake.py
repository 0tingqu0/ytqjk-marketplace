from __future__ import annotations

import hashlib
import sys
from dataclasses import replace
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.intake_extraction_contracts import (  # noqa: E402
    BlockKind, BoundingBox, CoordinateUnit, ExtractedBlock, ExtractedPage,
    ExtractedTable, ExtractionMode, ExtractionResult, ImageClassification,
    QualityStatus, RecognitionEvidence, RecognitionRound, TableCell,
)
from scripts.structured_document_intake import (  # noqa: E402
    CandidateState, GateDecision, GateState, IntakeBlockedError, SourceInput,
    StructuredDocumentIntake,
)


DIGEST_A = "a" * 64
DIGEST_B = "b" * 64


def _result(source: bytes, *, quality: QualityStatus = QualityStatus.ACCEPTABLE,
            reasons: tuple[str, ...] = ()) -> ExtractionResult:
    evidence = RecognitionEvidence("ocr", "v1", DIGEST_A)
    block = ExtractedBlock(
        "block-1", 1, 1, BlockKind.TEXT,
        BoundingBox(0, 0, 100, 20, CoordinateUnit.PIXELS),
        "正文内容。" * 800, ExtractionMode.OCR, 0.99, evidence,
    )
    page = ExtractedPage(
        1, 100, 100, CoordinateUnit.PIXELS, ExtractionMode.OCR, (block,),
    )
    return ExtractionResult(
        hashlib.sha256(source).hexdigest(), DIGEST_B, ExtractionMode.OCR,
        (RecognitionRound(1, ExtractionMode.OCR, evidence, 1),), (page,),
        quality, reasons, 1,
    )


class Extractor:
    def __init__(self, result: ExtractionResult) -> None:
        self.result = result

    def extract(self, source: bytes, source_name: str) -> ExtractionResult:
        del source, source_name
        return self.result


class Gate:
    def __init__(self, decision: GateDecision, ready: bool = True) -> None:
        self.decision = decision
        self.ready_value = ready

    def ready(self) -> bool:
        return self.ready_value

    def assess(self, *args: object) -> GateDecision:
        del args
        return self.decision


def _intake(source: bytes, result: ExtractionResult | None = None,
            assessment: GateDecision | None = None) -> StructuredDocumentIntake:
    return StructuredDocumentIntake(
        Extractor(result or _result(source)),
        Gate(GateDecision(GateState.CLEAR)),
        Gate(assessment or GateDecision(GateState.CLEAR)),
    )


def _source(content: bytes = b"document") -> SourceInput:
    return SourceInput(r"C:\secret\report.pdf", content, "项目归档")


def test_plan_is_deterministic_candidate_with_traceable_chunks() -> None:
    intake = _intake(b"document")
    first, second = intake.plan(_source()), intake.plan(_source())
    assert first == second
    assert first.state is CandidateState.CANDIDATE
    assert first.auto_approval_eligible is False
    assert first.provenance[0].source_name == "report.pdf"
    assert len(first.chunks) == 2
    assert all(len(chunk.text) <= 2000 for chunk in first.chunks)
    assert all(chunk.locator.page_number == 1 for chunk in first.chunks)
    assert first.metadata.engines[0]["config_digest"] == DIGEST_A
    assert "secret" not in repr(first)


def test_wrong_source_digest_and_missing_gate_fail_closed() -> None:
    source = b"document"
    forged = replace(_result(source), source_digest="f" * 64)
    with pytest.raises(IntakeBlockedError, match="SOURCE_DIGEST_MISMATCH"):
        _intake(source, forged).plan(_source(source))
    intake = StructuredDocumentIntake(Extractor(_result(source)), None, None)
    with pytest.raises(
        IntakeBlockedError, match="SOURCE_SECRET_SCAN_NOT_CONFIGURED"
    ):
        intake.plan(_source(source))


def test_review_conflict_keeps_candidate_and_disables_auto_approval() -> None:
    source = b"document"
    decision = GateDecision(GateState.REVIEW_REQUIRED, ("CONFLICT",))
    plan = _intake(source, assessment=decision).plan(_source(source))
    assert plan.state is CandidateState.CANDIDATE
    assert plan.auto_approval_eligible is False
    assert plan.metadata.review_reasons == (
        "ASSESSMENT_REVIEW_REQUIRED", "CONFLICT",
    )


def test_same_content_from_different_sources_has_stable_merge_key() -> None:
    source = b"document"
    first = _intake(source).plan(_source(source))
    other = _intake(source).plan(
        SourceInput("copy.pdf", source, "第二来源")
    )
    assert first.content_dedupe_key == other.content_dedupe_key
    assert first.provenance != other.provenance


def test_blocked_or_failed_assessment_never_returns_plan() -> None:
    source = b"document"
    blocked = GateDecision(GateState.BLOCKED, ("SECRET",))
    with pytest.raises(
        IntakeBlockedError, match="CANDIDATE_ASSESSMENT_BLOCKED"
    ):
        _intake(source, assessment=blocked).plan(_source(source))
    failed = StructuredDocumentIntake(
        Extractor(_result(source)), Gate(GateDecision(GateState.CLEAR)),
        Gate(GateDecision(GateState.CLEAR), ready=False),
    )
    with pytest.raises(
        IntakeBlockedError, match="CANDIDATE_ASSESSMENT_NOT_CONFIGURED"
    ):
        failed.plan(_source(source))


def test_custom_classifier_path_is_blocked_before_candidate_plan() -> None:
    source = b"document"
    result = _result(source)
    evidence = RecognitionEvidence("vision", "v1", DIGEST_A)
    classification = ImageClassification(
        "diagram", ("diagram",), r"C:\models\secret-summary", 0.99,
        evidence,
    )
    block = replace(
        result.pages[0].blocks[0], kind=BlockKind.IMAGE,
        image_classification=classification,
    )
    page = replace(result.pages[0], blocks=(block,))
    with pytest.raises(IntakeBlockedError, match="UNSAFE_IMAGE_SUMMARY_PATH"):
        _intake(source, replace(result, pages=(page,))).plan(_source(source))


def test_image_semantics_become_searchable_chunk_with_safe_engine_id() -> None:
    source = b"document"
    result = _result(source)
    evidence = RecognitionEvidence("docling/2.121.0", "vision/v1", DIGEST_A)
    classification = ImageClassification(
        "diagram",
        ("diagram", "flow-chart"),
        "流程从上传进入人工复核。",
        0.99,
        evidence,
        17,
    )
    block = replace(
        result.pages[0].blocks[0],
        kind=BlockKind.IMAGE,
        text=classification.summary,
        image_classification=classification,
        evidence=evidence,
    )
    page = replace(result.pages[0], blocks=(block,))
    plan = _intake(
        source,
        replace(result, pages=(page,)),
    ).plan(_source(source))
    assert len(plan.chunks) == 1
    assert "流程从上传进入人工复核" in plan.chunks[0].text
    assert "flow-chart" in plan.chunks[0].text
    image = plan.metadata.blocks[0]["image_classification"]
    assert image["elapsed_ms"] == 17
    assert image["evidence"]["engine"] == "docling/2.121.0"


def test_nested_subclasses_and_duplicate_cells_are_rejected() -> None:
    class ForgedBox(BoundingBox):
        pass

    source, result = b"document", _result(b"document")
    forged = replace(
        result.pages[0].blocks[0],
        coordinates=ForgedBox(0, 0, 100, 20, CoordinateUnit.PIXELS),
    )
    page = replace(result.pages[0], blocks=(forged,))
    with pytest.raises(IntakeBlockedError, match="EXTRACTION_CONTRACT_INVALID"):
        _intake(source, replace(result, pages=(page,))).plan(_source(source))
    box = BoundingBox(0, 0, 10, 10, CoordinateUnit.PIXELS)
    cells = (TableCell(0, 0, "a", box), TableCell(0, 0, "b", box))
    with pytest.raises(ValueError, match="overlapping"):
        ExtractedTable("table", box, 1, 1, cells)
