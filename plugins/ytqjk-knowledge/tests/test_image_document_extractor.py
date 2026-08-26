from __future__ import annotations

import base64
import hashlib
import sys
from dataclasses import dataclass, replace
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.image_document_extractor import (  # noqa: E402
    CLASSIFIER_ENGINE,
    NO_TEXT_REASON,
    SEMANTIC_DESCRIPTION_FAILED,
    ImageDocumentExtractor,
    ImageExtractionStatus,
    ImageFeatures,
)
from scripts.image_ocr_backend import (  # noqa: E402
    OcrBackendResult,
    OcrEngineEvidence,
    OcrNotConfigured,
    OcrPoint,
    OcrTextBlock,
)
from scripts.image_semantic_contract import DESCRIPTION_FAILED_TAG  # noqa: E402
from scripts.intake_extraction_contracts import (  # noqa: E402
    LOW_CONFIDENCE_REASON,
    BlockKind,
    CoordinateUnit,
    ImageClassification,
    QualityStatus,
    RecognitionEvidence,
    canonical_digest,
    canonical_json,
)


DIGEST_A = "a" * 64
DIGEST_B = "b" * 64
IMAGE = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lE"
    "QVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
)


def _quad(
    left: float = 10,
    top: float = 10,
    right: float = 100,
    bottom: float = 30,
) -> tuple[OcrPoint, OcrPoint, OcrPoint, OcrPoint]:
    return (
        OcrPoint(left, top),
        OcrPoint(right, top),
        OcrPoint(right, bottom),
        OcrPoint(left, bottom),
    )


def _block(
    text: str = "这是一段足够长的中文文档内容用于可靠分类",
    confidence: float = 0.98,
    quad: tuple[OcrPoint, OcrPoint, OcrPoint, OcrPoint] | None = None,
) -> OcrTextBlock:
    return OcrTextBlock(text, quad or _quad(), confidence)


def _result(blocks: tuple[OcrTextBlock, ...] | None = None) -> OcrBackendResult:
    evidence = OcrEngineEvidence("rapidocr", "3.9.2", DIGEST_A, DIGEST_B)
    return OcrBackendResult(
        200,
        100,
        (_block(),) if blocks is None else blocks,
        125,
        evidence,
    )


@dataclass
class FakeBackend:
    result: object

    def recognize(self, image_bytes: bytes) -> object:
        return self.result


class MissingBackend:
    def recognize(self, image_bytes: bytes) -> OcrBackendResult:
        raise OcrNotConfigured("NOT_CONFIGURED: OCR models are missing")


class TextSubclass(str):
    pass


class SemanticClassifier:
    def classify(
        self,
        image_bytes: bytes,
        features: ImageFeatures,
    ) -> ImageClassification:
        del image_bytes, features
        evidence = RecognitionEvidence("semantic-vision", "2", DIGEST_A)
        return ImageClassification(
            "diagram",
            ("diagram", "semantic"),
            "语义模型识别为流程图。",
            0.99,
            evidence,
        )


@dataclass
class FixedClassifier:
    result: object

    def classify(self, image_bytes: bytes, features: ImageFeatures) -> object:
        del image_bytes, features
        return self.result


def _classification(field: str, value: object) -> ImageClassification:
    result = ImageClassification(
        "diagram", ("diagram",), "流程图摘要", 0.99,
        RecognitionEvidence("semantic-vision", "2", DIGEST_A),
    )
    if field in {"engine", "model_version"}:
        evidence = replace(result.evidence, **{field: value})
        return replace(result, evidence=evidence)
    return replace(result, **{field: value})


def _extract(
    backend_result: object | None = None, *, filename: str = "document.png"
) -> object:
    selected = _result() if backend_result is None else backend_result
    return ImageDocumentExtractor(FakeBackend(selected)).extract(
        IMAGE,
        filename,
    )


def test_extraction_maps_text_geometry_confidence_and_evidence() -> None:
    raw = _block("  ＡＢＣ\r\n  知识\t库  ", 0.96, _quad(8, 6, 108, 36))
    outcome = _extract(_result((raw,)), filename=r"C:\secret\document.png")
    assert outcome.status is ImageExtractionStatus.SUCCEEDED
    result = outcome.result
    assert result is not None
    assert result.source_digest == hashlib.sha256(IMAGE).hexdigest()
    assert result.quality is QualityStatus.ACCEPTABLE
    assert result.review_reasons == ()
    assert result.elapsed_ms == 125
    image, text = result.pages[0].blocks
    assert image.kind is BlockKind.IMAGE
    assert image.image_classification is not None
    assert image.image_classification.category == "document"
    assert image.image_classification.evidence.engine == CLASSIFIER_ENGINE
    assert "heuristic" in image.image_classification.tags
    assert text.kind is BlockKind.TEXT
    assert text.text == "ABC 知识 库"
    assert text.confidence == 0.96
    assert text.coordinates.x == 8
    assert text.coordinates.y == 6
    assert text.coordinates.width == 100
    assert text.coordinates.height == 30
    assert text.coordinates.unit is CoordinateUnit.PIXELS
    assert text.evidence.engine == "rapidocr"
    assert text.evidence.model_version.startswith("3.9.2:")
    assert "secret" not in canonical_json(result)


def test_low_confidence_requires_review() -> None:
    outcome = _extract(_result((_block(confidence=0.87),)))
    assert outcome.result is not None
    assert outcome.result.quality is QualityStatus.REVIEW_REQUIRED
    assert outcome.result.review_reasons == (LOW_CONFIDENCE_REASON,)


def test_no_text_requires_review_and_keeps_image_block() -> None:
    outcome = _extract(_result(()), filename="camera_photo.jpg")
    assert outcome.result is not None
    assert outcome.result.quality is QualityStatus.REVIEW_REQUIRED
    assert outcome.result.review_reasons == (NO_TEXT_REASON,)
    blocks = outcome.result.pages[0].blocks
    assert len(blocks) == 1
    assert blocks[0].image_classification.category == "photo"


def test_blank_unknown_image_is_honest_review_without_text_blocks() -> None:
    outcome = _extract(_result(()), filename="blank.png")
    assert outcome.status is ImageExtractionStatus.SUCCEEDED
    assert outcome.result is not None
    assert outcome.result.review_reasons == (
        LOW_CONFIDENCE_REASON,
        NO_TEXT_REASON,
    )
    blocks = outcome.result.pages[0].blocks
    assert len(blocks) == 1
    assert blocks[0].kind is BlockKind.IMAGE
    assert blocks[0].image_classification.category == "unknown"


@pytest.mark.parametrize(
    ("filename", "blocks", "category"),
    (
        ("screenshot.png", (_block("短文本"),), "screenshot"),
        ("table.png", (_block("短文本"),), "table"),
        ("diagram.png", (_block("短文本"),), "diagram"),
        ("photo.jpg", (_block("短文本"),), "photo"),
        ("record.png", (_block(),), "document"),
        (
            "mystery.png",
            (_block("短", quad=_quad(10, 10, 20, 15)),),
            "unknown",
        ),
    ),
)
def test_heuristic_classifier_covers_required_categories(
    filename: str,
    blocks: tuple[OcrTextBlock, ...],
    category: str,
) -> None:
    outcome = _extract(_result(blocks), filename=filename)
    assert outcome.result is not None
    classification = outcome.result.pages[0].blocks[0].image_classification
    assert classification is not None
    assert classification.category == category
    assert classification.evidence.engine == CLASSIFIER_ENGINE
    assert classification.evidence.model_version.endswith("heuristic")


def test_layout_grid_classifies_table_without_filename_hint() -> None:
    blocks = (
        _block("a", quad=_quad(10, 10, 40, 25)),
        _block("b", quad=_quad(80, 10, 110, 25)),
        _block("c", quad=_quad(10, 50, 40, 65)),
        _block("d", quad=_quad(80, 50, 110, 65)),
    )
    outcome = _extract(_result(blocks), filename="data.png")
    classification = outcome.result.pages[0].blocks[0].image_classification
    assert classification.category == "table"


def test_semantic_classifier_port_replaces_heuristic_evidence() -> None:
    extractor = ImageDocumentExtractor(
        FakeBackend(_result()),
        classifier=SemanticClassifier(),
    )
    outcome = extractor.extract(IMAGE, "plain.png")
    assert outcome.result is not None
    classification = outcome.result.pages[0].blocks[0].image_classification
    assert classification.category == "diagram"
    assert classification.evidence.engine == "semantic-vision"


def test_description_fallback_keeps_image_and_requires_review() -> None:
    classification = replace(
        _classification("category", "diagram"),
        tags=("diagram", DESCRIPTION_FAILED_TAG),
    )
    outcome = ImageDocumentExtractor(
        _result_backend(),
        classifier=FixedClassifier(classification),
    ).extract(IMAGE, "plain.png")
    assert outcome.status is ImageExtractionStatus.SUCCEEDED
    assert outcome.result is not None
    assert outcome.result.quality is QualityStatus.REVIEW_REQUIRED
    assert outcome.result.review_reasons == (
        SEMANTIC_DESCRIPTION_FAILED,
    )


@pytest.mark.parametrize(
    ("field", "unsafe"),
    (
        ("category", r"C:\models\diagram"),
        ("category", "robot"),
        ("tags", (r"\\server\share",)),
        ("summary", "/home/victim/model"),
        ("summary", "api_key=" + "a" * 20),
        ("engine", r"C:\models\vision.onnx"),
        ("model_version", "ghp_" + "a" * 40),
        ("summary", TextSubclass("clean semantic summary")),
    ),
)
def test_untrusted_classifier_metadata_fails_before_canonical_result(
    field: str,
    unsafe: object,
) -> None:
    classifier = FixedClassifier(_classification(field, unsafe))
    extractor = ImageDocumentExtractor(_result_backend(), classifier=classifier)
    outcome = extractor.extract(IMAGE, "plain.png")
    assert outcome.status is ImageExtractionStatus.FAILED
    assert outcome.result is None
    assert "victim" not in outcome.reason
    assert "api_key" not in outcome.reason
    assert "ghp_" not in outcome.reason


def _result_backend() -> FakeBackend:
    return FakeBackend(_result())


def test_classifier_subclass_is_rejected() -> None:
    class ForgedClassification(ImageClassification):
        pass

    base = _classification("category", "diagram")
    forged = ForgedClassification(
        base.category, base.tags, base.summary, base.confidence, base.evidence,
    )
    extractor = ImageDocumentExtractor(
        _result_backend(), classifier=FixedClassifier(forged),
    )
    outcome = extractor.extract(IMAGE, "plain.png")
    assert outcome.status is ImageExtractionStatus.FAILED
    assert outcome.result is None


def test_backend_result_subclass_is_rejected() -> None:
    class ForgedBackendResult(OcrBackendResult):
        pass

    base = _result()
    forged = ForgedBackendResult(
        base.width, base.height, base.blocks, base.elapsed_ms, base.evidence,
    )
    outcome = _extract(forged)
    assert outcome.status is ImageExtractionStatus.FAILED


def test_ocr_body_absolute_path_is_left_for_later_intake_validation() -> None:
    body = r"C:\approved\manual.txt"
    outcome = _extract(_result((_block(body),)), filename="scan.png")
    assert outcome.status is ImageExtractionStatus.SUCCEEDED
    assert outcome.result is not None
    assert outcome.result.pages[0].blocks[1].text == body
    assert "manual.txt" in canonical_json(outcome.result)


def test_source_and_config_digests_are_stable() -> None:
    first = _extract()
    second = _extract()
    assert first.result is not None and second.result is not None
    assert first.result.source_digest == second.result.source_digest
    assert first.result.config_digest == second.result.config_digest
    assert canonical_digest(first.result) == canonical_digest(second.result)


def test_not_configured_is_explicit() -> None:
    outcome = ImageDocumentExtractor(MissingBackend()).extract(
        IMAGE, "x.png"
    )
    assert outcome.status is ImageExtractionStatus.NOT_CONFIGURED
    assert outcome.result is None
    assert outcome.reason.startswith("NOT_CONFIGURED")
