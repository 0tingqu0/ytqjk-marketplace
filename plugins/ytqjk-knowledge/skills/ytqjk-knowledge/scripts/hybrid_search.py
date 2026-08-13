"""Deterministic in-memory lexical/dense retrieval and RRF fusion."""

from __future__ import annotations

from dataclasses import replace
from time import monotonic
from typing import Protocol

from .dense_search import (
    DenseAdapter,
    DenseCorruptError,
    DenseIncompatibleError,
    DenseMatch,
    DenseUnavailableError,
    validate_dense_matches,
)
from .lexical_search import LexicalMatch, lexical_search
from .retrieval_contracts import (
    RetrievalResult,
    RetrievalContext,
    MAX_RESULT_LIMIT,
    ScoreBreakdown,
    SearchResponse,
    SnapshotChunk,
    validate_finite_number,
    validate_limit,
    validate_positive_int,
    validate_query,
    validate_snapshot,
)


class Reranker(Protocol):
    """Optional local reranker returning parent IDs in desired order."""

    def rerank(
        self, query: str, candidates: tuple[RetrievalResult, ...]
    ) -> tuple[str, ...]: ...


def hybrid_search(
    query: str,
    chunks: tuple[SnapshotChunk, ...],
    limit: int = 8,
    *,
    context: RetrievalContext,
    dense_adapter: DenseAdapter | None = None,
    reranker: Reranker | None = None,
    lexical_weight: float = 1.0,
    dense_weight: float = 1.0,
    rrf_k: int = 60,
    rerank_timeout: float = 1.0,
) -> SearchResponse:
    """Search one immutable snapshot without storage or network access."""
    text = validate_query(query)
    corpus = validate_snapshot(chunks, context)
    result_limit = validate_limit(limit)
    rrf_constant = validate_positive_int(rrf_k, "rrf_k")
    lexical_factor = validate_finite_number(
        lexical_weight,
        "lexical weight",
        nonnegative=True,
    )
    dense_factor = validate_finite_number(
        dense_weight,
        "dense weight",
        nonnegative=True,
    )
    timeout = validate_finite_number(
        rerank_timeout,
        "rerank timeout",
        positive=True,
    )
    if lexical_factor == dense_factor == 0:
        raise ValueError("at least one retrieval weight must be positive")

    lexical_limit = min(len(corpus), MAX_RESULT_LIMIT)
    dense, dense_status, reason = _dense_results(
        dense_adapter, text, corpus, len(corpus), dense_factor
    )
    degraded_dense = {"UNAVAILABLE", "ERROR", "INVALID"}
    fallback = dense_factor > 0 and dense_status in degraded_dense
    effective_lexical = lexical_factor
    if not effective_lexical and fallback:
        effective_lexical = 1.0
    lexical = (
        lexical_search(text, corpus, lexical_limit, context=context)
        if effective_lexical else ()
    )
    mode = _mode(lexical_factor, dense_factor, dense_status)
    fused = _fuse(
        corpus,
        lexical,
        dense,
        effective_lexical,
        dense_factor,
        rrf_constant,
        mode,
    )
    candidates = tuple(fused[:result_limit])
    results, reranker_status, rerank_reason = _rerank(
        text,
        candidates,
        reranker,
        timeout,
    )
    reasons = (reason, rerank_reason)
    degraded = "; ".join(item for item in reasons if item) or None
    status = "OK"
    if dense_status in degraded_dense and dense_factor:
        status = "DEGRADED_LEXICAL_ONLY"
    return SearchResponse(
        results,
        status,
        mode,
        dense_status,
        reranker_status,
        degraded,
    )


def _dense_results(
    adapter: DenseAdapter | None,
    query: str,
    chunks: tuple[SnapshotChunk, ...],
    limit: int,
    weight: float,
) -> tuple[tuple[DenseMatch, ...], str, str | None]:
    if adapter is None or weight == 0:
        reason = "dense adapter is not configured" if adapter is None else None
        return (), "UNAVAILABLE", reason
    try:
        matches = adapter.search(query, chunks, limit)
        return validate_dense_matches(matches, chunks, limit), "OK", None
    except (
        DenseUnavailableError,
        DenseCorruptError,
        DenseIncompatibleError,
    ) as error:
        return (), "ERROR", f"dense adapter error: {error}"
    except PermissionError:
        raise
    except OSError as error:
        return (), "ERROR", f"dense adapter os error: {error}"


def _mode(lexical_weight: float, dense_weight: float, dense_status: str) -> str:
    if dense_status != "OK":
        return "lexical"
    if lexical_weight == 0:
        return "dense"
    if dense_weight == 0:
        return "lexical"
    return "hybrid"


def _fuse(
    chunks: tuple[SnapshotChunk, ...],
    lexical: tuple[LexicalMatch, ...],
    dense: tuple[DenseMatch, ...],
    lexical_weight: float,
    dense_weight: float,
    rrf_k: int,
    mode: str,
) -> list[RetrievalResult]:
    by_id = {item.chunk_id: item for item in chunks}
    lexical_scores = {item.chunk_id: item.score for item in lexical}
    dense_scores = {item.chunk_id: item.score for item in dense}
    fused: dict[str, float] = {}
    for rank, match in enumerate(lexical, 1):
        fused[match.chunk_id] = (
            fused.get(match.chunk_id, 0.0)
            + lexical_weight / (rrf_k + rank)
        )
    for rank, match in enumerate(dense, 1):
        fused[match.chunk_id] = (
            fused.get(match.chunk_id, 0.0)
            + dense_weight / (rrf_k + rank)
        )
    ranked_ids = sorted(fused, key=lambda item_id: (-fused[item_id], item_id))
    parents: dict[str, RetrievalResult] = {}
    for chunk_id in ranked_ids:
        chunk = by_id[chunk_id]
        result = RetrievalResult(
            project_id=chunk.project_id,
            snapshot_id=chunk.snapshot_id,
            generation=chunk.generation,
            document_id=chunk.document_id,
            version_id=chunk.version_id,
            chunk_id=chunk.chunk_id,
            parent_id=chunk.parent_id,
            source=chunk.source,
            locator=chunk.locator,
            governance_state=chunk.governance_state,
            matched_content=chunk.content,
            fragment_locator=f"{chunk.locator}#chunk={chunk.chunk_id}",
            content=chunk.parent_content,
            content_hash=chunk.content_hash,
            scores=ScoreBreakdown(
                lexical=lexical_scores.get(chunk_id, 0.0),
                dense=dense_scores.get(chunk_id, 0.0),
                rrf=fused[chunk_id],
            ),
            retrieval_mode=mode,
        )
        current = parents.get(chunk.parent_id)
        if current is None or _result_key(result) < _result_key(current):
            parents[chunk.parent_id] = result
    return sorted(parents.values(), key=_result_key)


def _result_key(result: RetrievalResult) -> tuple[float, str, str]:
    return (-result.scores.rrf, result.parent_id, result.chunk_id)


def _rerank(
    query: str,
    candidates: tuple[RetrievalResult, ...],
    reranker: Reranker | None,
    timeout: float,
) -> tuple[tuple[RetrievalResult, ...], str, str | None]:
    if reranker is None or not candidates:
        return candidates, "NOT_CONFIGURED", None
    started = monotonic()
    try:
        order = reranker.rerank(query, candidates)
        if monotonic() - started > timeout:
            return candidates, "TIMEOUT", "reranker timed out"
        if not isinstance(order, tuple):
            raise TypeError("reranker output must be a tuple")
        expected = {item.parent_id for item in candidates}
        if len(order) != len(expected) or set(order) != expected:
            raise ValueError(
                "reranker must return every candidate parent id once"
            )
        by_parent = {item.parent_id: item for item in candidates}
        results = tuple(
            replace(
                by_parent[parent_id],
                scores=replace(
                    by_parent[parent_id].scores,
                    reranker=1 / rank,
                ),
            )
            for rank, parent_id in enumerate(order, 1)
        )
        return results, "OK", None
    except (TypeError, ValueError):
        raise
    except Exception as error:
        if monotonic() - started > timeout:
            return candidates, "TIMEOUT", "reranker timed out"
        return candidates, "ERROR", f"reranker error: {error}"
