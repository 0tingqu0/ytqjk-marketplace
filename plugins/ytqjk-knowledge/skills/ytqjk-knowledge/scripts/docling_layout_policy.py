"""Detect PDF layouts that need PP-StructureV3 review."""

from __future__ import annotations

from typing import Any

from scripts.intake_extraction_contracts import BlockKind


def complex_layout(blocks: list[Any]) -> bool:
    if any(block.kind is BlockKind.TABLE for block in blocks):
        return True
    text = [
        block.coordinates
        for block in blocks
        if block.kind is BlockKind.TEXT
    ]
    for index, first in enumerate(text):
        for second in text[index + 1:]:
            vertical = min(
                first.y + first.height,
                second.y + second.height,
            ) - max(first.y, second.y)
            separated = (
                first.x + first.width <= second.x
                or second.x + second.width <= first.x
            )
            if separated and vertical > 0:
                return True
    return False


__all__ = ["complex_layout"]
