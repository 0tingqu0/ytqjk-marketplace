"""Map Docling failures without exposing source paths or raw exceptions."""

from __future__ import annotations

from scripts.pdf_document_extractor import PdfExtractionError


def _error_text(error: BaseException) -> str:
    messages: list[str] = []
    seen: set[int] = set()
    current: BaseException | None = error
    while current is not None and id(current) not in seen:
        seen.add(id(current))
        messages.append(f"{type(current).__name__}: {current}")
        current = current.__cause__ or current.__context__
    return " ".join(messages).lower()


def map_docling_error(error: Exception) -> PdfExtractionError:
    message = _error_text(error)
    if "encrypt" in message or "password" in message:
        return PdfExtractionError(
            "PDF_ENCRYPTED",
            "encrypted PDF is blocked",
        )
    if "page" in message and (
        "limit" in message or "maximum" in message
    ):
        return PdfExtractionError(
            "PDF_TOO_MANY_PAGES",
            "PDF page limit exceeded",
        )
    if "model" in message or "artifact" in message:
        return PdfExtractionError(
            "NOT_CONFIGURED",
            "Docling model artifacts are unavailable",
        )
    return PdfExtractionError("PDF_CORRUPT", "Docling rejected the PDF")


__all__ = ["map_docling_error"]
