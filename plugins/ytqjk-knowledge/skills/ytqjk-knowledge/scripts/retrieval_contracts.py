"""Immutable contracts and fail-closed validation for snapshot retrieval."""

from __future__ import annotations

import hashlib
import math
import uuid
from dataclasses import dataclass
from typing import Literal


DenseStatus = Literal["UNAVAILABLE", "OK", "ERROR", "INVALID"]
RerankerStatus = Literal["NOT_CONFIGURED", "OK", "ERROR", "TIMEOUT", "INVALID"]
SearchStatus = Literal["OK", "DEGRADED_LEXICAL_ONLY"]
MAX_QUERY_LENGTH = 4096
MAX_RESULT_LIMIT = 100


@dataclass(frozen=True, slots=True)
class RetrievalContext:
    """Trusted caller context for one active project snapshot."""

    project_uuid: str
    active_snapshot_id: str
    generation: int


@dataclass(frozen=True, slots=True)
class SnapshotChunk:
    """Caller-supplied immutable child chunk with its returnable parent."""

    project_id: str
    snapshot_id: str
    generation: int
    document_id: str
    version_id: str
    chunk_id: str
    parent_id: str
    source: str
    locator: str
    governance_state: str
    snapshot_state: str
    snapshot_member: bool
    soft_deleted: bool
    content: str
    parent_content: str
    content_hash: str


@dataclass(frozen=True, slots=True)
class ScoreBreakdown:
    """Auditable scores used to produce one result."""

    lexical: float
    dense: float
    rrf: float
    reranker: float | None = None


@dataclass(frozen=True, slots=True)
class RetrievalResult:
    """Parent result with complete snapshot citation."""

    project_id: str
    snapshot_id: str
    generation: int
    document_id: str
    version_id: str
    chunk_id: str
    parent_id: str
    source: str
    locator: str
    governance_state: str
    matched_content: str
    fragment_locator: str
    content: str
    content_hash: str
    scores: ScoreBreakdown
    retrieval_mode: str


@dataclass(frozen=True, slots=True)
class ExpectedCitation:
    """Independent gold citation for one expected child result."""

    project_id: str
    snapshot_id: str
    generation: int
    document_id: str
    version_id: str
    chunk_id: str
    parent_id: str
    governance_state: str
    source: str
    locator: str
    content_hash: str
    snippet: str


@dataclass(frozen=True, slots=True)
class SearchResponse:
    """Results plus explicit optional-component state."""

    results: tuple[RetrievalResult, ...]
    status: SearchStatus
    mode: str
    dense_status: DenseStatus
    reranker_status: RerankerStatus
    degraded_reason: str | None = None


def validate_query(query: object) -> str:
    """Return normalized non-empty query."""
    if not isinstance(query, str) or not query.strip():
        raise ValueError("query must be non-empty text")
    normalized = query.strip()
    if len(normalized) > MAX_QUERY_LENGTH:
        raise ValueError(f"query exceeds {MAX_QUERY_LENGTH} characters")
    return normalized


def validate_positive_int(value: object, name: str) -> int:
    """Reject booleans and non-positive integer parameters."""
    if isinstance(value, bool) or not isinstance(value, int):
        raise TypeError(f"{name} must be an integer")
    if value <= 0:
        raise ValueError(f"{name} must be positive")
    return value


def validate_limit(value: object) -> int:
    """Return result limit within public API bound."""
    limit = validate_positive_int(value, "limit")
    if limit > MAX_RESULT_LIMIT:
        raise ValueError(f"limit exceeds {MAX_RESULT_LIMIT}")
    return limit


def validate_finite_number(
    value: object,
    name: str,
    *,
    positive: bool = False,
    nonnegative: bool = False,
) -> float:
    """Return finite float satisfying requested range."""
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise TypeError(f"{name} must be numeric")
    number = float(value)
    if not math.isfinite(number):
        raise ValueError(f"{name} must be finite")
    if positive and number <= 0:
        raise ValueError(f"{name} must be positive")
    if nonnegative and number < 0:
        raise ValueError(f"{name} must be nonnegative")
    return number


def validate_snapshot(
    chunks: object, context: RetrievalContext
) -> tuple[SnapshotChunk, ...]:
    """Validate one-project, one-snapshot immutable corpus."""
    _validate_context(context)
    if not isinstance(chunks, tuple):
        raise TypeError("snapshot chunks must be a tuple")
    if not chunks:
        raise ValueError("snapshot chunks must not be empty")
    if any(not isinstance(item, SnapshotChunk) for item in chunks):
        raise TypeError("snapshot contains invalid chunk")
    typed = chunks
    chunk_ids: set[str] = set()
    parents: dict[str, tuple[object, ...]] = {}
    for item in typed:
        _validate_chunk(item)
        if item.project_id != context.project_uuid:
            raise ValueError("project does not match trusted context")
        if item.snapshot_id != context.active_snapshot_id:
            raise ValueError("snapshot does not match trusted active snapshot")
        if item.generation != context.generation:
            raise ValueError("generation does not match trusted context")
        if item.chunk_id in chunk_ids:
            raise ValueError("duplicate chunk id")
        chunk_ids.add(item.chunk_id)
        parent = (
            item.project_id,
            item.snapshot_id,
            item.generation,
            item.document_id,
            item.version_id, item.source, item.locator, item.governance_state,
            item.snapshot_state, item.snapshot_member, item.soft_deleted,
            item.parent_content, item.content_hash,
        )
        if item.parent_id in parents and parents[item.parent_id] != parent:
            raise ValueError("parent metadata mismatch")
        parents[item.parent_id] = parent
    return typed


def _validate_context(context: object) -> None:
    if not isinstance(context, RetrievalContext):
        raise TypeError("trusted retrieval context is required")
    _uuid(context.project_uuid, "project_uuid")
    _uuid(context.active_snapshot_id, "active_snapshot_id")
    validate_positive_int(context.generation, "generation")


def _uuid(value: object, name: str) -> None:
    if not isinstance(value, str):
        raise ValueError(f"{name} must be UUID")
    try:
        parsed = uuid.UUID(value)
    except (ValueError, AttributeError) as error:
        raise ValueError(f"{name} must be UUID") from error
    if str(parsed) != value:
        raise ValueError(f"{name} must be canonical UUID")


def _validate_chunk(item: SnapshotChunk) -> None:
    fields = (
        item.project_id, item.snapshot_id, item.document_id, item.version_id,
        item.chunk_id,
        item.parent_id,
        item.source,
        item.locator,
        item.governance_state,
        item.snapshot_state, item.content,
        item.parent_content, item.content_hash,
    )
    if any(not isinstance(value, str) or not value.strip() for value in fields):
        raise ValueError("snapshot text fields must be non-empty")
    validate_positive_int(item.generation, "generation")
    if item.governance_state not in {"approved", "verified"}:
        raise ValueError("governance state is not retrievable")
    if item.snapshot_state != "ACTIVE":
        raise ValueError("snapshot state is not active")
    valid_membership = isinstance(item.snapshot_member, bool)
    valid_deletion = isinstance(item.soft_deleted, bool)
    if not valid_membership or not valid_deletion:
        raise TypeError("membership flags must be boolean")
    if not item.snapshot_member:
        raise ValueError("chunk is not a snapshot member")
    if item.soft_deleted:
        raise ValueError("soft-deleted document is not retrievable")
    digest = hashlib.sha256(item.parent_content.encode("utf-8")).hexdigest()
    if item.content_hash != digest:
        raise ValueError("content hash mismatch")
    if item.content not in item.parent_content:
        raise ValueError("child content is not contained by parent")


def citation_valid(
    result: RetrievalResult,
    corpus: tuple[SnapshotChunk, ...],
    context: RetrievalContext,
    expected_citations: tuple[ExpectedCitation, ...],
) -> bool:
    """Check result against trusted corpus and independent gold."""
    matches = [
        item
        for item in corpus
        if item.chunk_id == result.chunk_id
    ]
    if len(matches) != 1:
        return False
    item = matches[0]
    corpus_valid = (
        result.project_id == context.project_uuid == item.project_id
        and result.snapshot_id
        == context.active_snapshot_id
        == item.snapshot_id
        and result.generation == context.generation == item.generation
        and result.document_id == item.document_id
        and result.version_id == item.version_id
        and result.parent_id == item.parent_id
        and result.governance_state == item.governance_state
        and result.source == item.source
        and result.locator == item.locator
        and result.content_hash == item.content_hash
        and result.content == item.parent_content
        and result.matched_content == item.content
        and result.fragment_locator
        == f"{item.locator}#chunk={item.chunk_id}"
        and result.content_hash
        == hashlib.sha256(
            item.parent_content.encode("utf-8")
        ).hexdigest()
    )
    return corpus_valid and any(
        _matches_gold(result, expected)
        for expected in expected_citations
    )


def _matches_gold(
    result: RetrievalResult,
    expected: ExpectedCitation,
) -> bool:
    return (
        result.project_id == expected.project_id
        and result.snapshot_id == expected.snapshot_id
        and result.generation == expected.generation
        and result.document_id == expected.document_id
        and result.version_id == expected.version_id
        and result.chunk_id == expected.chunk_id
        and result.parent_id == expected.parent_id
        and result.governance_state == expected.governance_state
        and result.source == expected.source
        and result.locator == expected.locator
        and result.content_hash == expected.content_hash
        and result.matched_content == expected.snippet
    )
