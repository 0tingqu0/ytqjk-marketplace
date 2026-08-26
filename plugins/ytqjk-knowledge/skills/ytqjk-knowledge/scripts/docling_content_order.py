"""Resolve Docling body/group references into deterministic content order."""

from __future__ import annotations

from typing import Any

from scripts.intake_extraction_contracts import BlockKind
from scripts.pdf_document_extractor import PdfExtractionError


def _corrupt(message: str) -> PdfExtractionError:
    return PdfExtractionError("PDF_CORRUPT", message)


def ordered_content(
    payload: dict[str, Any],
) -> tuple[tuple[BlockKind, dict[str, Any]], ...]:
    content: dict[str, tuple[BlockKind, dict[str, Any]]] = {}
    fallback: list[tuple[str, BlockKind, dict[str, Any]]] = []
    kinds = (
        ("texts", BlockKind.TEXT),
        ("tables", BlockKind.TABLE),
        ("pictures", BlockKind.IMAGE),
    )
    for name, kind in kinds:
        for index, item in enumerate(payload.get(name) or ()):
            if not isinstance(item, dict):
                continue
            reference = str(item.get("self_ref") or f"#/{name}/{index}")
            content[reference] = kind, item
            fallback.append((reference, kind, item))
    groups = {
        str(item.get("self_ref") or f"#/groups/{index}"): item
        for index, item in enumerate(payload.get("groups") or ())
        if isinstance(item, dict)
    }
    ordered: list[tuple[BlockKind, dict[str, Any]]] = []
    seen: set[str] = set()
    visiting: set[str] = set()

    def walk(node: object) -> None:
        if not isinstance(node, dict):
            return
        reference = node.get("$ref")
        if isinstance(reference, str):
            if reference in content and reference not in seen:
                seen.add(reference)
                ordered.append(content[reference])
            elif reference in groups:
                if reference in visiting:
                    raise _corrupt("cyclic Docling content group")
                visiting.add(reference)
                walk(groups[reference])
                visiting.remove(reference)
            return
        for child in node.get("children") or ():
            walk(child)

    walk(payload.get("body"))
    ordered.extend(
        (kind, item)
        for reference, kind, item in fallback
        if reference not in seen
    )
    return tuple(ordered)


__all__ = ["ordered_content"]
