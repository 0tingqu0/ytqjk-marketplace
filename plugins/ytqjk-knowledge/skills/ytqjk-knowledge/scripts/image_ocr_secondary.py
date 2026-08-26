from __future__ import annotations

import math
import re
import unicodedata
from dataclasses import dataclass

from scripts.image_ocr_backend import (
    OcrBackend,
    OcrBackendError,
    OcrBackendResult,
    OcrNotConfigured,
)
from scripts.intake_security import LocalScanner


SECONDARY_NOT_CONFIGURED = "SECONDARY_OCR_NOT_CONFIGURED"
SECONDARY_FAILED = "SECONDARY_OCR_FAILED"
SECONDARY_CONFLICT = "SECONDARY_OCR_CONFLICT"
SECONDARY_RECOVERED_NO_TEXT = "SECONDARY_OCR_RECOVERED_NO_TEXT"
LOW_BLOCK_RATIO = 0.20
_ABSOLUTE_PATH = re.compile(
    r"(?:[A-Za-z]:[\\/]|\\\\|(?<![A-Za-z0-9:])\/[^\s]+)"
)
_SCANNER = LocalScanner()


@dataclass(frozen=True)
class SecondaryOcrDecision:
    selected: OcrBackendResult
    rounds: tuple[OcrBackendResult, ...]
    review_reasons: tuple[str, ...]


def needs_secondary(result: OcrBackendResult, threshold: float) -> bool:
    _validate_threshold(threshold)
    _validate_result(result, "primary")
    if not result.blocks:
        return True
    low = sum(
        block.confidence < threshold for block in result.blocks
    )
    short_page = len(result.blocks) <= 4 and low > 0
    dense_low = low / len(result.blocks) >= LOW_BLOCK_RATIO
    return short_page or dense_low


def run_secondary_ocr(
    image_bytes: bytes,
    primary: OcrBackendResult,
    secondary: OcrBackend,
    threshold: float,
) -> SecondaryOcrDecision:
    if not isinstance(image_bytes, bytes) or not image_bytes:
        raise ValueError("image input must be non-empty bytes")
    if not needs_secondary(primary, threshold):
        return SecondaryOcrDecision(primary, (primary,), ())
    try:
        candidate = secondary.recognize(image_bytes)
    except OcrNotConfigured:
        return SecondaryOcrDecision(
            primary,
            (primary,),
            (SECONDARY_NOT_CONFIGURED,),
        )
    except OcrBackendError:
        return SecondaryOcrDecision(
            primary,
            (primary,),
            (SECONDARY_FAILED,),
        )
    _validate_result(candidate, "secondary")
    _same_geometry(primary, candidate)
    rounds = (primary, candidate)
    recovered = (
        not primary.blocks
        and bool(candidate.blocks)
        and _score(candidate) >= threshold
    )
    if recovered:
        return SecondaryOcrDecision(
            candidate,
            rounds,
            (SECONDARY_RECOVERED_NO_TEXT,),
        )
    if _texts(primary) != _texts(candidate):
        return SecondaryOcrDecision(
            primary,
            rounds,
            (SECONDARY_CONFLICT,),
        )
    selected = candidate if _score(candidate) > _score(primary) else primary
    return SecondaryOcrDecision(selected, rounds, ())


def _validate_threshold(value: float) -> None:
    valid = type(value) in (int, float) and math.isfinite(value)
    if not valid or value < 0 or value > 1:
        raise ValueError("secondary OCR threshold must be between 0 and 1")


def _validate_result(result: object, label: str) -> None:
    if type(result) is not OcrBackendResult:
        raise TypeError(f"{label} OCR returned an invalid result type")
    evidence = result.evidence
    for name in (evidence.engine, evidence.package_version):
        if type(name) is not str or not name.strip() or len(name) > 160:
            raise ValueError(f"unsafe {label} OCR evidence")
        if _ABSOLUTE_PATH.search(name):
            raise ValueError(f"unsafe {label} OCR evidence path")
        scan = _SCANNER.scan(name.encode("utf-8"), "ocr-evidence")
        if scan.state.value != "CLEAN":
            raise ValueError(f"unsafe {label} OCR evidence secret")
    for block in result.blocks:
        xs = [point.x for point in block.quad]
        ys = [point.y for point in block.quad]
        valid = (
            min(xs) >= 0
            and min(ys) >= 0
            and max(xs) <= result.width
            and max(ys) <= result.height
            and max(xs) > min(xs)
            and max(ys) > min(ys)
        )
        if not valid:
            raise ValueError(f"unsafe {label} OCR geometry")


def _same_geometry(
    primary: OcrBackendResult,
    secondary: OcrBackendResult,
) -> None:
    if (
        primary.width != secondary.width
        or primary.height != secondary.height
    ):
        raise ValueError("secondary OCR image geometry does not match primary")


def _texts(result: OcrBackendResult) -> tuple[str, ...]:
    return tuple(_normalized(block.text) for block in result.blocks)


def _normalized(value: str) -> str:
    normalized = unicodedata.normalize("NFKC", value)
    return " ".join(normalized.casefold().split())


def _score(result: OcrBackendResult) -> float:
    return min((block.confidence for block in result.blocks), default=0.0)
