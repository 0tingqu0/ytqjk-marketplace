"""Stable PDF result digest and explicit manual-review policy."""

from __future__ import annotations

import hashlib
import json
from typing import Protocol

from scripts.intake_extraction_contracts import LOW_CONFIDENCE_REASON


PICTURE_DESCRIPTION_NOT_CONFIGURED = (
    "PDF_PICTURE_DESCRIPTION_NOT_CONFIGURED"
)
PICTURE_DESCRIPTION_UNVERIFIED = "PDF_PICTURE_DESCRIPTION_UNVERIFIED"
SECONDARY_OCR_NOT_CONFIGURED = "PDF_SECONDARY_OCR_NOT_CONFIGURED"
OCR_CONFIDENCE_UNREPORTED = "PDF_OCR_CONFIDENCE_UNREPORTED"


class Limits(Protocol):
    max_bytes: int
    max_pages: int
    native_min_characters: int
    review_threshold: float


def configuration_digest(
    limits: Limits,
    dedup_iou_threshold: float,
    native_digest: str,
    ocr_digest: str | None,
    secondary_digest: str | None = None,
) -> str:
    payload = {
        "limits": {
            "dedup_iou_threshold": dedup_iou_threshold,
            "max_bytes": limits.max_bytes,
            "max_pages": limits.max_pages,
            "native_min_characters": limits.native_min_characters,
            "review_threshold": limits.review_threshold,
        },
        "native": native_digest,
        "ocr": ocr_digest,
        "secondary": secondary_digest,
    }
    encoded = json.dumps(
        payload,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def review_reasons(
    low_confidence: bool,
    picture_present: bool,
    picture_described: bool,
    *,
    secondary_attempted: bool = False,
    confidence_unreported: bool = False,
) -> tuple[str, ...]:
    reasons = []
    if low_confidence:
        reasons.append(LOW_CONFIDENCE_REASON)
        if confidence_unreported:
            reasons.append(OCR_CONFIDENCE_UNREPORTED)
        elif not secondary_attempted:
            reasons.append(SECONDARY_OCR_NOT_CONFIGURED)
    if picture_present and not picture_described:
        reasons.append(PICTURE_DESCRIPTION_NOT_CONFIGURED)
    if picture_described:
        reasons.append(PICTURE_DESCRIPTION_UNVERIFIED)
    return tuple(reasons)


__all__ = [
    "PICTURE_DESCRIPTION_NOT_CONFIGURED",
    "PICTURE_DESCRIPTION_UNVERIFIED",
    "OCR_CONFIDENCE_UNREPORTED",
    "SECONDARY_OCR_NOT_CONFIGURED",
    "configuration_digest",
    "review_reasons",
]
