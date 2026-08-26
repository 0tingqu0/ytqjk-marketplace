"""Select page-level Paddle results without hiding OCR conflicts."""

from __future__ import annotations

import unicodedata
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    from scripts.pdf_document_extractor import BackendDocument, BackendPage
else:
    BackendDocument = Any
    BackendPage = Any


CONFLICT = "PDF_SECONDARY_OCR_CONFLICT"
RECOVERED_NO_TEXT = "PDF_SECONDARY_OCR_RECOVERED_NO_TEXT"
STRUCTURE_USED = "PDF_PP_STRUCTURE_V3_REVIEW_REQUIRED"


@dataclass(frozen=True)
class PdfSecondaryDecision:
    pages: dict[int, BackendPage]
    reasons: tuple[str, ...]


def retry_page_numbers(
    pages: tuple[BackendPage, ...],
    complex_pages: tuple[int, ...],
    threshold: float,
) -> tuple[int, ...]:
    complex_set = set(complex_pages)
    return tuple(
        page.number
        for page in pages
        if page.number in complex_set
        or not _texts(page)
        or _measured_score(page) < threshold
    )


def choose_secondary_pages(
    primary: BackendDocument,
    secondary: BackendDocument,
    complex_pages: tuple[int, ...],
    threshold: float,
) -> PdfSecondaryDecision:
    first = {page.number: page for page in primary.pages}
    second = {page.number: page for page in secondary.pages}
    if set(second) - set(first):
        raise ValueError("secondary PDF page does not exist in primary")
    complex_set = set(complex_pages)
    selected = dict(first)
    reasons = []
    for number, candidate in second.items():
        current = first[number]
        if number in complex_set:
            selected[number] = candidate
            reasons.append(STRUCTURE_USED)
            continue
        current_text = _texts(current)
        candidate_text = _texts(candidate)
        recovered = (
            not current_text
            and bool(candidate_text)
            and _score(candidate) >= threshold
        )
        if recovered:
            selected[number] = candidate
            reasons.append(RECOVERED_NO_TEXT)
        elif current_text != candidate_text:
            reasons.append(CONFLICT)
        elif _score(candidate) > _score(current):
            selected[number] = candidate
    return PdfSecondaryDecision(
        selected,
        tuple(dict.fromkeys(reasons)),
    )


def _texts(page: BackendPage) -> tuple[str, ...]:
    return tuple(
        _normalize(block.text)
        for block in page.blocks
        if block.text.strip()
    )


def _normalize(value: str) -> str:
    normalized = unicodedata.normalize("NFKC", value)
    return " ".join(normalized.casefold().split())


def _score(page: BackendPage) -> float:
    return min((block.confidence for block in page.blocks), default=0.0)


def _measured_score(page: BackendPage) -> float:
    scores = [block.confidence for block in page.blocks]
    if scores and all(score == 0 for score in scores):
        return 1.0
    return min(scores, default=0.0)


__all__ = [
    "CONFLICT",
    "PdfSecondaryDecision",
    "RECOVERED_NO_TEXT",
    "STRUCTURE_USED",
    "choose_secondary_pages",
    "retry_page_numbers",
]
