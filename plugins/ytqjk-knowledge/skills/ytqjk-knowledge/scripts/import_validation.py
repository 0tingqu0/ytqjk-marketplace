"""Boundary validation for bootstrap imports."""

from __future__ import annotations

import re
from pathlib import PurePosixPath
from typing import Sequence

from .import_contracts import CandidateImport
from .intake_governance import CandidateRegistry


_SAFE_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}\Z")


def validate_candidates(
    candidates: Sequence[CandidateImport],
) -> tuple[CandidateImport, ...]:
    """Revalidate scan, chunk, and candidate governance proofs."""
    if isinstance(candidates, (str, bytes)):
        raise TypeError("candidates must be a sequence")
    items = tuple(candidates)
    registry = CandidateRegistry(allow_extensionless_text=True)
    for item in items:
        if not isinstance(item, CandidateImport):
            raise TypeError("candidate import is invalid")
        text(item.title, "candidate title")
        name(item.source_kind, "source kind")
        if item.governance_state != "CANDIDATE":
            raise ValueError("bootstrap governance must be CANDIDATE")
        registry.add(
            "00000000-0000-0000-0000-000000000001",
            item.parsed,
        )
        source_ref(item.parsed.source.relative_path)
    return items


def name(value: object, field: str) -> str:
    """Validate a non-sensitive stable identifier."""
    if not isinstance(value, str) or _SAFE_NAME.fullmatch(value) is None:
        raise ValueError(f"{field} is invalid")
    return value


def text(value: object, field: str) -> str:
    """Validate required display text."""
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{field} is required")
    return value.strip()


def source_ref(value: object) -> str:
    """Accept only source-root-relative POSIX provenance."""
    if (
        not isinstance(value, str)
        or not value
        or "\\" in value
        or ":" in value
    ):
        raise ValueError("source reference is invalid")
    path = PurePosixPath(value)
    invalid = any(part in {"", ".", ".."} for part in path.parts)
    if path.is_absolute() or invalid:
        raise ValueError("source reference is invalid")
    return path.as_posix()
