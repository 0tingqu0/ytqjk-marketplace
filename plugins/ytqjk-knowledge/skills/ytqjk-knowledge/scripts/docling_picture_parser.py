"""Validate Docling picture-description annotations for indexing."""

from __future__ import annotations

from typing import Any

from scripts.image_description_backend import parse_description_output
from scripts.intake_extraction_contracts import (
    CONFIDENCE_THRESHOLD,
    ImageClassification,
    RecognitionEvidence,
)
from scripts.pdf_document_extractor import PdfExtractionError


def _description_text(item: dict[str, Any]) -> object:
    meta = item.get("meta")
    if isinstance(meta, dict):
        description = meta.get("description")
        if isinstance(description, dict) and "text" in description:
            return description["text"]
    for annotation in item.get("annotations") or ():
        if isinstance(annotation, dict) and "text" in annotation:
            return annotation["text"]
    return None


def picture_classification(
    item: dict[str, Any],
    evidence: RecognitionEvidence | None,
) -> ImageClassification | None:
    raw = _description_text(item)
    if raw is None:
        return None
    if evidence is None:
        raise PdfExtractionError(
            "BACKEND_FAILURE",
            "unexpected PDF picture description",
        )
    try:
        summary, tags = parse_description_output(raw)
    except ValueError:
        return None
    return ImageClassification(
        "embedded_picture",
        tags,
        summary,
        CONFIDENCE_THRESHOLD,
        evidence,
        0,
    )


__all__ = ["picture_classification"]
