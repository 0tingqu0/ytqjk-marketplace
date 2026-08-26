from __future__ import annotations

import base64
import sys
from dataclasses import dataclass
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.image_document_extractor import (  # noqa: E402
    ImageDocumentExtractor,
    ImageExtractionStatus,
)
from scripts.image_ocr_backend import (  # noqa: E402
    OcrBackendError,
    OcrBackendResult,
    OcrEngineEvidence,
    OcrNotConfigured,
    OcrPoint,
    OcrTextBlock,
)
from scripts.image_ocr_secondary import (  # noqa: E402
    SECONDARY_CONFLICT,
    SECONDARY_FAILED,
    SECONDARY_NOT_CONFIGURED,
    SECONDARY_RECOVERED_NO_TEXT,
    run_secondary_ocr,
)
from scripts.intake_extraction_contracts import (  # noqa: E402
    LOW_CONFIDENCE_REASON,
    QualityStatus,
)


DIGEST_A = "a" * 64
DIGEST_B = "b" * 64
IMAGE = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lE"
    "QVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
)


def _block(text: str, confidence: float) -> OcrTextBlock:
    return OcrTextBlock(
        text,
        (
            OcrPoint(10, 10),
            OcrPoint(190, 10),
            OcrPoint(190, 30),
            OcrPoint(10, 30),
        ),
        confidence,
    )


def _result(
    engine: str,
    text: str = "准确识别的中文文档内容足够用于稳定分类",
    confidence: float = 0.87,
    elapsed_ms: int = 10,
) -> OcrBackendResult:
    blocks = () if not text else (_block(text, confidence),)
    evidence = OcrEngineEvidence(
        engine,
        "3.0.0",
        DIGEST_A,
        DIGEST_B,
    )
    return OcrBackendResult(200, 100, blocks, elapsed_ms, evidence)


@dataclass
class Backend:
    result: object
    calls: int = 0

    def recognize(self, image_bytes: bytes) -> object:
        self.calls += 1
        return self.result


class MissingBackend:
    def recognize(self, image_bytes: bytes) -> OcrBackendResult:
        raise OcrNotConfigured("NOT_CONFIGURED: local model missing")


class BrokenBackend:
    def recognize(self, image_bytes: bytes) -> OcrBackendResult:
        raise OcrBackendError("secondary inference failed")


def test_high_confidence_does_not_invoke_secondary() -> None:
    primary = _result("rapidocr", confidence=0.99)
    secondary = Backend(_result("paddleocr"))
    decision = run_secondary_ocr(b"image", primary, secondary, 0.88)
    assert secondary.calls == 0
    assert decision.selected is primary
    assert decision.rounds == (primary,)
    assert decision.review_reasons == ()


def test_isolated_low_block_in_large_page_stays_manual_review() -> None:
    blocks = tuple(
        _block(str(index), 0.50 if index == 0 else 0.99)
        for index in range(10)
    )
    primary = OcrBackendResult(
        200,
        100,
        blocks,
        10,
        OcrEngineEvidence(
            "rapidocr",
            "3.0.0",
            DIGEST_A,
            DIGEST_B,
        ),
    )
    secondary = Backend(_result("paddleocr"))
    decision = run_secondary_ocr(b"image", primary, secondary, 0.88)
    assert secondary.calls == 0
    assert decision.selected is primary


def test_consistent_higher_confidence_selects_secondary() -> None:
    primary = _result("rapidocr", confidence=0.70, elapsed_ms=11)
    secondary = _result("paddleocr", confidence=0.97, elapsed_ms=22)
    decision = run_secondary_ocr(
        b"image",
        primary,
        Backend(secondary),
        0.88,
    )
    assert decision.selected is secondary
    assert decision.rounds == (primary, secondary)
    assert decision.review_reasons == ()


def test_conflict_keeps_primary_and_marks_review() -> None:
    primary = _result("rapidocr", "正确文本", 0.70)
    secondary = _result("paddleocr", "冲突文本", 0.99)
    decision = run_secondary_ocr(
        b"image",
        primary,
        Backend(secondary),
        0.88,
    )
    assert decision.selected is primary
    assert decision.rounds == (primary, secondary)
    assert decision.review_reasons == (SECONDARY_CONFLICT,)


def test_missing_secondary_is_explicit_and_keeps_primary() -> None:
    primary = _result("rapidocr", confidence=0.70)
    decision = run_secondary_ocr(
        b"image",
        primary,
        MissingBackend(),
        0.88,
    )
    assert decision.selected is primary
    assert decision.rounds == (primary,)
    assert decision.review_reasons == (SECONDARY_NOT_CONFIGURED,)


def test_failed_secondary_is_reviewable_and_keeps_primary() -> None:
    primary = _result("rapidocr", confidence=0.70)
    decision = run_secondary_ocr(
        b"image",
        primary,
        BrokenBackend(),
        0.88,
    )
    assert decision.selected is primary
    assert decision.rounds == (primary,)
    assert decision.review_reasons == (SECONDARY_FAILED,)


def test_high_confidence_secondary_recovers_empty_primary() -> None:
    primary = _result("rapidocr", text="")
    secondary = _result("paddleocr", confidence=0.97)
    decision = run_secondary_ocr(
        b"image",
        primary,
        Backend(secondary),
        0.88,
    )
    assert decision.selected is secondary
    assert decision.rounds == (primary, secondary)
    assert decision.review_reasons == (SECONDARY_RECOVERED_NO_TEXT,)


def test_low_confidence_secondary_does_not_replace_empty_primary() -> None:
    primary = _result("rapidocr", text="")
    secondary = _result("paddleocr", confidence=0.70)
    decision = run_secondary_ocr(
        b"image",
        primary,
        Backend(secondary),
        0.88,
    )
    assert decision.selected is primary
    assert decision.review_reasons == (SECONDARY_CONFLICT,)


@pytest.mark.parametrize(
    "secondary",
    (
        object(),
        OcrBackendResult(
            200,
            100,
            (
                OcrTextBlock(
                    "越界",
                    (
                        OcrPoint(10, 10),
                        OcrPoint(210, 10),
                        OcrPoint(210, 30),
                        OcrPoint(10, 30),
                    ),
                    0.99,
                ),
            ),
            1,
            OcrEngineEvidence(
                "paddleocr",
                "3.0.0",
                DIGEST_A,
                DIGEST_B,
            ),
        ),
        _result(r"C:\models\paddleocr", confidence=0.99),
        _result("ghp_" + "a" * 40, confidence=0.99),
    ),
)
def test_untrusted_secondary_fails_closed(secondary: object) -> None:
    with pytest.raises((TypeError, ValueError)):
        run_secondary_ocr(
            b"image",
            _result("rapidocr", confidence=0.70),
            Backend(secondary),
            0.88,
        )


def test_extractor_records_both_rounds_and_elapsed_time() -> None:
    primary = _result("rapidocr", confidence=0.70, elapsed_ms=11)
    secondary = _result("paddleocr", confidence=0.97, elapsed_ms=22)
    extractor = ImageDocumentExtractor(
        Backend(primary),
        secondary_backend=Backend(secondary),
    )
    outcome = extractor.extract(IMAGE, "document.png")
    assert outcome.status is ImageExtractionStatus.SUCCEEDED
    assert outcome.result is not None
    assert [item.evidence.engine for item in outcome.result.rounds] == [
        "rapidocr",
        "paddleocr",
    ]
    assert outcome.result.elapsed_ms == 33
    assert outcome.result.pages[0].blocks[1].confidence == 0.97
    assert outcome.result.quality is QualityStatus.ACCEPTABLE


def test_extractor_conflict_and_missing_secondary_are_reviewable() -> None:
    primary = _result("rapidocr", "首轮文本", 0.70)
    conflict = ImageDocumentExtractor(
        Backend(primary),
        secondary_backend=Backend(
            _result("paddleocr", "另一文本", 0.99)
        ),
    ).extract(IMAGE, "document.png")
    assert conflict.result is not None
    assert conflict.result.review_reasons == (
        SECONDARY_CONFLICT,
        LOW_CONFIDENCE_REASON,
    )
    assert conflict.result.pages[0].blocks[1].text == "首轮文本"
    missing = ImageDocumentExtractor(
        Backend(primary),
        secondary_backend=MissingBackend(),
    ).extract(IMAGE, "document.png")
    assert missing.result is not None
    assert missing.result.review_reasons == (
        SECONDARY_NOT_CONFIGURED,
        LOW_CONFIDENCE_REASON,
    )
