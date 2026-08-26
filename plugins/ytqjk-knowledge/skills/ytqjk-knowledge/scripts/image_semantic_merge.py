"""Merge local visual type and description into one safe classification."""

from __future__ import annotations

import hashlib
import json
from typing import Protocol

from scripts.image_description_backend import ImageDescription
from scripts.image_document_extractor import ImageFeatures
from scripts.image_ocr_backend import OcrNotConfigured
from scripts.image_semantic_contract import DESCRIPTION_FAILED_TAG
from scripts.intake_extraction_contracts import (
    ImageClassification,
    RecognitionEvidence,
)


class Classifier(Protocol):
    def classify(
        self,
        image_bytes: bytes,
        features: ImageFeatures,
    ) -> ImageClassification: ...


class Describer(Protocol):
    def describe(self, image_bytes: bytes) -> ImageDescription: ...


class MergedImageSemanticClassifier:
    def __init__(self, classifier: Classifier, describer: Describer) -> None:
        self._classifier = classifier
        self._describer = describer

    def classify(
        self,
        image_bytes: bytes,
        features: ImageFeatures,
    ) -> ImageClassification:
        typed = self._classifier.classify(image_bytes, features)
        if type(typed) is not ImageClassification:
            raise ValueError("image classifier result is invalid")
        try:
            described = self._describer.describe(image_bytes)
        except (OcrNotConfigured, ValueError):
            return _description_fallback(typed)
        if type(described) is not ImageDescription:
            raise ValueError("image description result is invalid")
        tags = tuple(dict.fromkeys((*typed.tags, *described.tags, "smolvlm")))
        if len(tags) > 16:
            tags = tags[:16]
        evidence = RecognitionEvidence(
            "ytqjk-local-vision",
            "DocumentFigureClassifier-v2.5+SmolVLM-256M-Instruct",
            _digest({
                "classifier": typed.evidence.config_digest,
                "describer": described.evidence.config_digest,
            }),
        )
        summary = f"{typed.summary} 图片描述：{described.summary}"
        if len(summary) > 2000:
            raise ValueError("merged image summary is too long")
        return ImageClassification(
            typed.category,
            tags,
            summary,
            typed.confidence,
            evidence,
            typed.elapsed_ms + described.elapsed_ms,
        )


def _description_fallback(
    typed: ImageClassification,
) -> ImageClassification:
    tags = tuple(dict.fromkeys((*typed.tags, DESCRIPTION_FAILED_TAG)))[:16]
    summary = (
        f"{typed.summary} 图片描述未返回可验证结果，已转人工复审。"
    )
    if len(summary) > 2000:
        raise ValueError("merged image summary is too long")
    return ImageClassification(
        typed.category,
        tags,
        summary,
        typed.confidence,
        typed.evidence,
        typed.elapsed_ms,
    )


def _digest(value: object) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


__all__ = ["MergedImageSemanticClassifier"]
