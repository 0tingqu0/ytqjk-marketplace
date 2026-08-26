"""Fail-closed planning from source bytes to candidate knowledge DTOs."""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import asdict, dataclass, is_dataclass
from enum import Enum
from pathlib import PurePosixPath, PureWindowsPath
from typing import Callable, Protocol

from .intake_extraction_contracts import (
    ExtractionResult,
    QualityStatus,
    canonical_digest,
    canonical_json,
)
from .structured_document_chunks import (
    StructuredChunk,
    build_structured_chunks,
    validate_structured_result,
)


_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_ABSOLUTE_PATH = re.compile(
    r"(?:[A-Za-z]:[\\/]|\\\\|"
    r"(?<![\w:])/(?:home|users|var|etc|tmp|opt|root|mnt|srv|usr)/)",
    re.IGNORECASE,
)


class CandidateState(str, Enum):
    CANDIDATE = "CANDIDATE"


class GateState(str, Enum):
    CLEAR, REVIEW_REQUIRED, BLOCKED = (
        "CLEAR", "REVIEW_REQUIRED", "BLOCKED")


@dataclass(frozen=True)
class GateDecision:
    state: GateState
    reasons: tuple[str, ...] = ()

    def __post_init__(self) -> None:
        if not isinstance(self.state, GateState):
            raise ValueError("gate state is invalid")
        if any(not isinstance(reason, str) or not reason.strip()
               for reason in self.reasons):
            raise ValueError("gate reason is invalid")


class SecretScannerPort(Protocol):
    ready: Callable[[], bool]
    assess: Callable[[bytes, str], GateDecision]


class CandidateAssessmentPort(Protocol):
    ready: Callable[[], bool]
    assess: Callable[["CandidatePlan"], GateDecision]


class DocumentExtractor(Protocol):
    extract: Callable[[bytes, str], ExtractionResult]


class IntakeBlockedError(RuntimeError): pass


@dataclass(frozen=True)
class SourceInput:
    source_name: str
    content: bytes
    purpose: str


@dataclass(frozen=True)
class SourceProvenance:
    source_name: str
    source_digest: str
    purpose: str


@dataclass(frozen=True)
class CandidateMetadata:
    title: str
    summary: str
    tags: tuple[str, ...]
    pages: tuple[dict[str, object], ...]
    blocks: tuple[dict[str, object], ...]
    engines: tuple[dict[str, str], ...]
    review_reasons: tuple[str, ...]


@dataclass(frozen=True)
class CandidatePlan:
    candidate_id: str
    idempotency_key: str
    content_dedupe_key: str
    state: CandidateState
    auto_approval_eligible: bool
    source_digest: str
    extraction_digest: str
    extraction_config_digest: str
    provenance: tuple[SourceProvenance, ...]
    metadata: CandidateMetadata
    chunks: tuple[StructuredChunk, ...]


def _digest(value: object) -> str:
    raw = json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True).encode("utf-8")
    return hashlib.sha256(raw).hexdigest()


def _required_text(value: object, name: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{name} must be non-empty text")
    return value.strip()


def _safe_source_name(value: object) -> str:
    raw = _required_text(value, "source_name")
    name = PurePosixPath(PureWindowsPath(raw).name).name.strip()
    if not name or name in {".", ".."}:
        raise ValueError("source_name must contain a basename")
    return name


def _public_text(value: str, field: str) -> str:
    text = _required_text(value, field)
    if _ABSOLUTE_PATH.search(text):
        raise IntakeBlockedError(f"UNSAFE_{field.upper()}_PATH")
    return text


def _gate(
    gate: SecretScannerPort | CandidateAssessmentPort | None,
    operation: str,
    *args: object,
) -> GateDecision:
    if gate is None:
        raise IntakeBlockedError(f"{operation}_NOT_CONFIGURED")
    try:
        ready = gate.ready()
        decision = gate.assess(*args)  # type: ignore[arg-type]
    except Exception as error:
        raise IntakeBlockedError(f"{operation}_FAILED") from error
    if ready is not True or not isinstance(decision, GateDecision):
        raise IntakeBlockedError(f"{operation}_NOT_CONFIGURED")
    if decision.state is GateState.BLOCKED:
        raise IntakeBlockedError(f"{operation}_BLOCKED")
    return decision


def _box_value(box: object) -> dict[str, object]:
    return {
        "height": getattr(box, "height"),
        "unit": getattr(box, "unit").value,
        "width": getattr(box, "width"),
        "x": getattr(box, "x"),
        "y": getattr(box, "y"),
    }


def _evidence_value(evidence: object) -> dict[str, str]:
    return {
        "config_digest": getattr(evidence, "config_digest"),
        "engine": _public_text(getattr(evidence, "engine"), "engine"),
        "model_version": _public_text(
            getattr(evidence, "model_version"), "model_version"
        ),
    }


def _block_values(result: ExtractionResult) -> tuple[dict[str, object], ...]:
    values: list[dict[str, object]] = []
    for page in result.pages:
        for block in page.blocks:
            tables = tuple({
                "cells": tuple({
                    "bounding_box": _box_value(cell.coordinates),
                    "column": cell.column,
                    "column_span": cell.column_span,
                    "row": cell.row,
                    "row_span": cell.row_span,
                    "text": cell.text,
                } for cell in table.cells),
                "id": table.table_id,
                "bounding_box": _box_value(table.coordinates),
            } for table in block.tables)
            classification = block.image_classification
            image = None if classification is None else {
                "category": classification.category,
                "confidence": classification.confidence,
                "elapsed_ms": classification.elapsed_ms,
                "evidence": _evidence_value(classification.evidence),
                "summary": _public_text(
                    classification.summary, "image_summary"
                ),
                "tags": tuple(
                    _public_text(tag, "image_tag")
                    for tag in classification.tags
                ),
            }
            values.append({
                "block_id": block.block_id,
                "bounding_box": _box_value(block.coordinates),
                "confidence": block.confidence,
                "evidence": _evidence_value(block.evidence),
                "image_classification": image,
                "kind": block.kind.value,
                "page_number": page.number,
                "tables": tables,
            })
    return tuple(values)


def _metadata(result: ExtractionResult, name: str) -> CandidateMetadata:
    classifications = [
        block.image_classification
        for page in result.pages
        for block in page.blocks
        if block.image_classification is not None
    ]
    tags = sorted({
        _public_text(tag, "image_tag")
        for item in classifications for tag in item.tags
    })
    pages = tuple({
        "height": page.height,
        "number": page.number,
        "unit": page.coordinate_unit.value,
        "width": page.width,
    } for page in result.pages)
    engines = tuple(_evidence_value(item.evidence) for item in result.rounds)
    summary = "；".join(
        _public_text(item.summary, "image_summary")
        for item in classifications
    ).strip()
    if not summary:
        text = next((block.text.strip() for page in result.pages
                     for block in page.blocks if block.text.strip()), "")
        summary = text[:240] or "提取失败，等待人工复核。"
    title = _public_text(name.rsplit(".", 1)[0], "title")
    return CandidateMetadata(
        title=title,
        summary=summary,
        tags=tuple(tags),
        pages=pages,
        blocks=_block_values(result),
        engines=engines,
        review_reasons=result.review_reasons,
    )


class StructuredDocumentIntake:
    def __init__(
        self,
        extractor: DocumentExtractor,
        secret_scanner: SecretScannerPort | None,
        assessor: CandidateAssessmentPort | None,
    ) -> None:
        self._extractor = extractor
        self._secret_scanner = secret_scanner
        self._assessor = assessor

    def plan(self, source: SourceInput) -> CandidatePlan:
        name = _safe_source_name(source.source_name)
        purpose = _required_text(source.purpose, "purpose")
        if not isinstance(source.content, bytes) or not source.content:
            raise ValueError("content must be non-empty bytes")
        source_digest = hashlib.sha256(source.content).hexdigest()
        _gate(self._secret_scanner, "SOURCE_SECRET_SCAN", source.content,
              "source")
        try:
            result = validate_structured_result(
                self._extractor.extract(source.content, name)
            )
            result_digest = canonical_digest(result)
            canonical_json(result)
        except Exception as error:
            raise IntakeBlockedError("EXTRACTION_CONTRACT_INVALID") from error
        if result.source_digest != source_digest:
            raise IntakeBlockedError("SOURCE_DIGEST_MISMATCH")
        if not _SHA256.fullmatch(result.config_digest):
            raise IntakeBlockedError("EXTRACTION_CONFIG_INVALID")
        chunks = build_structured_chunks(result)
        metadata = _metadata(result, name)
        provisional = CandidatePlan(
            candidate_id="", idempotency_key="", content_dedupe_key="",
            state=CandidateState.CANDIDATE, auto_approval_eligible=False,
            source_digest=source_digest, extraction_digest=result_digest,
            extraction_config_digest=result.config_digest,
            provenance=(SourceProvenance(name, source_digest, purpose),),
            metadata=metadata, chunks=chunks,
        )
        _gate(self._secret_scanner, "CANDIDATE_SECRET_SCAN",
              self._candidate_bytes(provisional), "candidate")
        assessment = _gate(
            self._assessor, "CANDIDATE_ASSESSMENT", provisional
        )
        reasons = set(result.review_reasons) | set(assessment.reasons)
        if result.quality is QualityStatus.FAILED:
            reasons.add("EXTRACTION_FAILED")
        if assessment.state is GateState.REVIEW_REQUIRED:
            reasons.add("ASSESSMENT_REVIEW_REQUIRED")
        content_key = _digest([chunk.digest for chunk in chunks])
        idempotency = _digest({
            "config": result.config_digest,
            "result": result_digest,
            "source": source_digest,
        })
        candidate_id = hashlib.sha256(
            f"structured-candidate-v1:{idempotency}".encode("utf-8")
        ).hexdigest()
        final_metadata = CandidateMetadata(
            title=metadata.title, summary=metadata.summary, tags=metadata.tags,
            pages=metadata.pages, blocks=metadata.blocks,
            engines=metadata.engines,
            review_reasons=tuple(sorted(reasons)),
        )
        return CandidatePlan(
            candidate_id=candidate_id, idempotency_key=idempotency,
            content_dedupe_key=content_key, state=CandidateState.CANDIDATE,
            auto_approval_eligible=False, source_digest=source_digest,
            extraction_digest=result_digest,
            extraction_config_digest=result.config_digest,
            provenance=provisional.provenance, metadata=final_metadata,
            chunks=chunks,
        )

    @staticmethod
    def _candidate_bytes(candidate: CandidatePlan) -> bytes:
        def encode(value: object) -> object:
            if isinstance(value, Enum):
                return value.value
            if is_dataclass(value) and not isinstance(value, type):
                return asdict(value)
            name = type(value).__name__
            raise TypeError(f"unsupported candidate value: {name}")

        text = json.dumps(
            candidate, default=encode, ensure_ascii=False,
            sort_keys=True,
        )
        return text.encode("utf-8")
