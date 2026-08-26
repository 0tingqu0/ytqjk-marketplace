from __future__ import annotations

import sys
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.image_description_backend import (  # noqa: E402
    ImageDescription,
)
from scripts.image_document_extractor import ImageFeatures  # noqa: E402
from scripts.image_ocr_backend import OcrNotConfigured  # noqa: E402
from scripts.image_semantic_contract import (  # noqa: E402
    DESCRIPTION_FAILED_TAG,
)
from scripts.image_semantic_merge import (  # noqa: E402
    MergedImageSemanticClassifier,
)
from scripts.intake_extraction_contracts import (  # noqa: E402
    ImageClassification,
    RecognitionEvidence,
)


EVIDENCE = RecognitionEvidence("local", "v1", "a" * 64)


class FakeClassifier:
    def classify(
        self,
        image_bytes: bytes,
        features: ImageFeatures,
    ) -> ImageClassification:
        assert image_bytes == b"image"
        assert features.text_sample == "PNG"
        return ImageClassification(
            "chart",
            ("chart",),
            "图表分类",
            0.9,
            EVIDENCE,
            5,
        )


class FakeDescriber:
    def describe(self, image_bytes: bytes) -> ImageDescription:
        assert image_bytes == b"image"
        return ImageDescription(
            "折线图显示销量上升",
            ("销量", "chart"),
            7,
            EVIDENCE,
        )


def _features() -> ImageFeatures:
    return ImageFeatures("a.png", 10, 10, 0, 0, "PNG")


def test_merge_makes_description_and_tags_searchable() -> None:
    merger = MergedImageSemanticClassifier(
        FakeClassifier(),
        FakeDescriber(),
    )
    result = merger.classify(b"image", _features())
    assert result.category == "chart"
    assert result.tags == ("chart", "销量", "smolvlm")
    assert "销量上升" in result.summary
    assert result.elapsed_ms == 12
    assert result.evidence.engine == "ytqjk-local-vision"
    assert len(result.evidence.config_digest) == 64


@pytest.mark.parametrize("bad", (None, object()))
def test_merge_rejects_invalid_description_contract(bad: object) -> None:
    class BadDescriber:
        def describe(self, image_bytes: bytes) -> object:
            return bad

    merger = MergedImageSemanticClassifier(
        FakeClassifier(),
        BadDescriber(),  # type: ignore[arg-type]
    )
    with pytest.raises(ValueError, match="description result"):
        merger.classify(b"image", _features())


@pytest.mark.parametrize(
    "error",
    (
        ValueError("invalid structured output"),
        OcrNotConfigured("NOT_CONFIGURED: model unavailable"),
    ),
)
def test_description_failure_keeps_classifier_and_marks_review(
    error: Exception,
) -> None:
    class FailedDescriber:
        def describe(self, image_bytes: bytes) -> ImageDescription:
            raise error

    result = MergedImageSemanticClassifier(
        FakeClassifier(),
        FailedDescriber(),
    ).classify(b"image", _features())
    assert result.category == "chart"
    assert result.tags == ("chart", DESCRIPTION_FAILED_TAG)
    assert "人工复审" in result.summary
    assert result.evidence == EVIDENCE
