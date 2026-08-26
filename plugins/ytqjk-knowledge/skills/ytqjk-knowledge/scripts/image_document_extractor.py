from __future__ import annotations

import hashlib
import json
import math
import re
from dataclasses import dataclass
from enum import Enum
from pathlib import PurePosixPath, PureWindowsPath
from typing import Protocol

from scripts.image_ocr_backend import (
    OcrBackend, OcrBackendResult, OcrEngineEvidence, OcrNotConfigured,
)
from scripts.image_document_materializer import (
    ImageFeatures, contract_blocks, image_features, located_blocks,
)
from scripts.image_input_guard import validate_image_input
from scripts.image_ocr_secondary import run_secondary_ocr
from scripts.image_semantic_contract import DESCRIPTION_FAILED_TAG
from scripts.intake_extraction_contracts import (
    CONFIDENCE_THRESHOLD, LOW_CONFIDENCE_REASON, CoordinateUnit,
    ExtractedPage, ExtractionMode, ExtractionResult, ImageClassification,
    QualityStatus, RecognitionEvidence, RecognitionRound,
)
from scripts.intake_security import LocalScanner


NO_TEXT_REASON = "NO_TEXT_DETECTED"
SEMANTIC_DESCRIPTION_FAILED = "SEMANTIC_DESCRIPTION_FAILED"
CLASSIFIER_ENGINE = "ytqjk-image-heuristic"
CLASSIFIER_VERSION = "1.0.0-heuristic"
HEURISTIC_TOKENS = {
    "screenshot": ("screenshot", "screen", "截屏", "截图"),
    "table": ("table", "sheet", "excel", "表格", "清单"),
    "diagram": ("diagram", "chart", "flow", "架构", "流程", "示意"),
    "photo": ("photo", "camera", "dsc", "照片", "相机", "img_"),
    "document": ("document", "文档", "doc_"),
}
_ALLOWED_CATEGORIES = frozenset((*HEURISTIC_TOKENS, "unknown"))
_PATH_PATTERN = (
    r"(?:[A-Za-z]:[\\/]|\\\\|"
    r"(?<![\w:])/(?:home|users|var|etc|tmp|opt|root|mnt|srv|usr)/)"
)
_ABSOLUTE_PATH = re.compile(_PATH_PATTERN, re.IGNORECASE)
_METADATA_SCANNER = LocalScanner()


def _digest(value: object) -> str:
    encoded = json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _safe_name(filename: object) -> str:
    if not isinstance(filename, str) or not filename.strip():
        raise ValueError("filename must be non-empty text")
    name = PurePosixPath(PureWindowsPath(filename).name).name.strip()
    if not name:
        raise ValueError("filename must contain a basename")
    return name


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def _safe_metadata(value: object, name: str, limit: int) -> str:
    valid = type(value) is str and bool(value.strip())
    _require(valid and len(value) <= limit, f"unsafe {name}")
    _require(_ABSOLUTE_PATH.search(value) is None, f"unsafe {name} path")
    scan = _METADATA_SCANNER.scan(value.encode("utf-8"), "classification")
    _require(scan.state.value == "CLEAN", f"unsafe {name} secret")
    return value.strip()


def _validated_classification(value: object) -> ImageClassification:
    _require(type(value) is ImageClassification, "invalid classification type")
    category = _safe_metadata(value.category, "category", 64)
    _require(category in _ALLOWED_CATEGORIES, "unsupported image category")
    _require(type(value.tags) is tuple, "invalid classification tags")
    _require(1 <= len(value.tags) <= 16, "invalid classification tags")
    tags = tuple(_safe_metadata(tag, "tag", 64) for tag in value.tags)
    evidence = value.evidence
    _require(type(evidence) is RecognitionEvidence, "invalid evidence type")
    confidence = value.confidence
    number = type(confidence) in (int, float) and math.isfinite(confidence)
    _require(number and 0 <= confidence <= 1, "invalid classification score")
    _require(type(evidence.config_digest) is str, "invalid config digest")
    engine = _safe_metadata(evidence.engine, "engine", 160)
    model = _safe_metadata(evidence.model_version, "model", 160)
    safe_evidence = RecognitionEvidence(engine, model, evidence.config_digest)
    summary = _safe_metadata(value.summary, "summary", 2000)
    elapsed = value.elapsed_ms
    valid_elapsed = (
        isinstance(elapsed, int)
        and not isinstance(elapsed, bool)
        and 0 <= elapsed <= 2**63 - 1
    )
    _require(valid_elapsed, "invalid classification elapsed time")
    return ImageClassification(
        category,
        tags,
        summary,
        float(confidence),
        safe_evidence,
        elapsed,
    )


class ImageExtractionStatus(str, Enum):
    SUCCEEDED, NOT_CONFIGURED, FAILED = "SUCCEEDED", "NOT_CONFIGURED", "FAILED"


class SemanticImageClassifier(Protocol):
    def classify(
        self, image_bytes: bytes, features: ImageFeatures,
    ) -> ImageClassification: ...


@dataclass(frozen=True)
class ImageExtractionOutcome:
    status: ImageExtractionStatus
    result: ExtractionResult | None
    reason: str
    engine_evidence: OcrEngineEvidence | None


class HeuristicImageClassifier:
    _config_digest = _digest((CLASSIFIER_VERSION, HEURISTIC_TOKENS))

    def classify(
        self, image_bytes: bytes, features: ImageFeatures,
    ) -> ImageClassification:
        del image_bytes
        category, confidence = self._decision(features)
        text_tag = "ocr-text" if features.block_count else "no-text"
        summary = (
            f"启发式分类为 {category}; 识别 {features.block_count} 个文本块, "
            f"{features.character_count} 个字符。"
        )
        evidence = RecognitionEvidence(
            CLASSIFIER_ENGINE, CLASSIFIER_VERSION, self._config_digest,
        )
        return ImageClassification(
            category, (category, text_tag, "heuristic"), summary,
            confidence, evidence,
        )

    @staticmethod
    def _decision(features: ImageFeatures) -> tuple[str, float]:
        lowered = features.filename.casefold()
        for category in HEURISTIC_TOKENS:
            if any(token in lowered for token in HEURISTIC_TOKENS[category]):
                return category, 0.93
        grid = features.block_count >= 4
        grid &= features.row_count >= 2 and features.column_count >= 2
        if grid:
            return "table", 0.91
        words = ("->", "→", "流程", "节点", "开始", "结束")
        if any(word in features.text_sample for word in words):
            return "diagram", 0.89
        dense = features.character_count >= 16 or features.block_count >= 3
        return ("document", 0.92) if dense else ("unknown", 0.35)


class ImageDocumentExtractor:
    def __init__(
        self,
        backend: OcrBackend,
        *,
        classifier: SemanticImageClassifier | None = None,
        review_threshold: float = CONFIDENCE_THRESHOLD,
        secondary_backend: OcrBackend | None = None,
    ) -> None:
        valid = (
            isinstance(review_threshold, (int, float))
            and not isinstance(review_threshold, bool)
            and math.isfinite(review_threshold)
            and CONFIDENCE_THRESHOLD <= review_threshold <= 1
        )
        if not valid:
            raise ValueError("review_threshold must be between 0.88 and 1")
        self._backend = backend
        self._classifier = classifier or HeuristicImageClassifier()
        self._review_threshold = float(review_threshold)
        self._secondary_backend = secondary_backend

    def extract(
        self, image_bytes: bytes, filename: str,
    ) -> ImageExtractionOutcome:
        try:
            validate_image_input(image_bytes)
            result = self._backend.recognize(image_bytes)
            if type(result) is not OcrBackendResult:
                raise TypeError("OCR backend returned an invalid result type")
            selected, rounds, reasons = result, (result,), ()
            if self._secondary_backend is not None:
                decision = run_secondary_ocr(
                    image_bytes, result, self._secondary_backend,
                    self._review_threshold,
                )
                selected = decision.selected
                rounds = decision.rounds
                reasons = decision.review_reasons
            extraction = self._build(
                image_bytes, _safe_name(filename), selected, rounds, reasons,
            )
            return ImageExtractionOutcome(
                ImageExtractionStatus.SUCCEEDED, extraction, "",
                selected.evidence,
            )
        except OcrNotConfigured as error:
            return ImageExtractionOutcome(
                ImageExtractionStatus.NOT_CONFIGURED, None, str(error), None,
            )
        except Exception as error:
            reason = f"EXTRACTION_FAILED:{type(error).__name__}"
            return ImageExtractionOutcome(
                ImageExtractionStatus.FAILED, None, reason, None,
            )

    def _build(
        self,
        image_bytes: bytes,
        filename: str,
        backend: OcrBackendResult,
        rounds: tuple[OcrBackendResult, ...],
        extra_reasons: tuple[str, ...],
    ) -> ExtractionResult:
        source_digest = hashlib.sha256(image_bytes).hexdigest()
        evidence = self._contract_evidence(backend)
        located = located_blocks(backend)
        features = image_features(filename, backend, located)
        raw_classification = self._classifier.classify(image_bytes, features)
        classification = _validated_classification(raw_classification)
        blocks = contract_blocks(
            backend, located, evidence, classification,
        )
        reasons = list(extra_reasons)
        if DESCRIPTION_FAILED_TAG in classification.tags:
            reasons.append(SEMANTIC_DESCRIPTION_FAILED)
        if any(block.confidence < self._review_threshold for block in blocks):
            reasons.append(LOW_CONFIDENCE_REASON)
        if not located:
            reasons.append(NO_TEXT_REASON)
        quality = QualityStatus.REVIEW_REQUIRED if reasons else (
            QualityStatus.ACCEPTABLE
        )
        page = ExtractedPage(
            1, backend.width, backend.height, CoordinateUnit.PIXELS,
            ExtractionMode.OCR, blocks,
        )
        round_configs = tuple(
            item.evidence.config_digest for item in rounds
        )
        config_values = (
            round_configs[0] if len(round_configs) == 1 else round_configs,
            classification.evidence.config_digest,
            self._review_threshold,
        )
        config_digest = _digest(config_values)
        round_items = tuple(
            RecognitionRound(
                ordinal, ExtractionMode.OCR,
                self._contract_evidence(item), item.elapsed_ms,
            )
            for ordinal, item in enumerate(rounds, 1)
        )
        return ExtractionResult(
            source_digest, config_digest, ExtractionMode.OCR, round_items,
            (page,), quality, tuple(reasons),
            sum(item.elapsed_ms for item in rounds)
            + classification.elapsed_ms,
        )

    @staticmethod
    def _contract_evidence(backend: OcrBackendResult) -> RecognitionEvidence:
        version = backend.evidence.package_version
        version += f":sha256:{backend.evidence.model_digest}"
        return RecognitionEvidence(
            backend.evidence.engine, version,
            backend.evidence.config_digest,
        )
