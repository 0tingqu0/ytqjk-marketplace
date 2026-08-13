"""Injectable local dense-search boundary with strict output validation."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Protocol

from .retrieval_contracts import SnapshotChunk, validate_finite_number


class DenseUnavailableError(RuntimeError):
    """Local vector index or model is unavailable."""


class DenseCorruptError(RuntimeError):
    """Local vector data failed integrity validation."""


class DenseIncompatibleError(RuntimeError):
    """Local vector data is incompatible with its adapter."""


@dataclass(frozen=True, slots=True)
class DenseMatch:
    """Adapter-produced score for one known child chunk."""

    chunk_id: str
    score: float


class DenseAdapter(Protocol):
    """Local adapter; implementations own model lifecycle."""

    def search(
        self, query: str, chunks: tuple[SnapshotChunk, ...], limit: int
    ) -> tuple[DenseMatch, ...]: ...

    def config_fingerprint(self) -> str:
        """Return stable model/index/config identity; never instance repr."""
        ...


def validate_dense_matches(
    matches: object, chunks: tuple[SnapshotChunk, ...], limit: int
) -> tuple[DenseMatch, ...]:
    """Reject malformed, duplicate, unknown, or non-finite adapter output."""
    if not isinstance(matches, tuple):
        raise TypeError("dense adapter output must be a tuple")
    if len(matches) > limit:
        raise ValueError("dense adapter returned more than limit")
    known = {item.chunk_id for item in chunks}
    seen: set[str] = set()
    validated: list[DenseMatch] = []
    for match in matches:
        if not isinstance(match, DenseMatch):
            raise TypeError("dense adapter returned invalid match")
        if match.chunk_id not in known:
            raise ValueError("dense adapter returned unknown chunk id")
        if match.chunk_id in seen:
            raise ValueError("dense adapter returned duplicate id")
        seen.add(match.chunk_id)
        score = validate_finite_number(match.score, "dense score")
        validated.append(DenseMatch(match.chunk_id, score))
    validated.sort(key=lambda match: (-match.score, match.chunk_id))
    return tuple(validated)
