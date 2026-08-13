"""Deterministic offline retrieval evaluation over fixed corpora."""

from __future__ import annotations

import hashlib
import json
import math
from dataclasses import dataclass
from typing import Literal, Mapping

from .dense_search import DenseAdapter
from .hybrid_search import hybrid_search
from .retrieval_contracts import (
    ExpectedCitation,
    RetrievalContext,
    SnapshotChunk,
    citation_valid as _citation_valid,
    validate_limit,
    validate_query,
    validate_snapshot,
)


RetrievalMode = Literal["lexical", "dense", "hybrid"]


@dataclass(frozen=True, slots=True)
class EvaluationCase:
    """Fixed query and relevant parent document IDs."""

    query: str
    relevant_document_ids: frozenset[str]
    expected_citations: tuple[ExpectedCitation, ...]


@dataclass(frozen=True, slots=True)
class EvaluationMetrics:
    """Aggregate metrics for one retrieval mode."""

    recall_at_k: float
    mrr: float
    ndcg_at_k: float
    citation_completeness: float
    citation_validity: float
    project_leakage: float
    candidate_leakage_count: int
    query_count: int
    corpus_fingerprint: str
    snapshot_fingerprint: str
    config_fingerprint: str
    evaluation_fingerprint: str


def evaluate_retrieval(
    cases: tuple[EvaluationCase, ...],
    corpus: tuple[SnapshotChunk, ...],
    *,
    context: RetrievalContext,
    mode: RetrievalMode = "lexical",
    dense_adapter: DenseAdapter | None = None,
    k: int = 5,
) -> EvaluationMetrics:
    """Evaluate one mode without network, files, DB, or external services."""
    snapshot = validate_snapshot(corpus, context)
    cutoff = validate_limit(k)
    _validate_cases(cases)
    if mode not in ("lexical", "dense", "hybrid"):
        raise ValueError("unsupported retrieval mode")
    if mode in ("dense", "hybrid") and dense_adapter is None:
        raise ValueError("dense adapter is required for requested mode")
    recalls: list[float] = []
    reciprocals: list[float] = []
    discounts: list[float] = []
    matched_gold = 0
    expected_gold = 0
    valid_citations = 0
    result_count = 0
    leaked = 0
    candidate_leakage = 0
    project_id = snapshot[0].project_id
    for case in cases:
        response = hybrid_search(
            case.query,
            snapshot,
            limit=cutoff,
            dense_adapter=dense_adapter,
            context=context,
            lexical_weight=0.0 if mode == "dense" else 1.0,
            dense_weight=0.0 if mode == "lexical" else 1.0,
        )
        if mode != "lexical" and response.status != "OK":
            raise RuntimeError(
                f"{mode} evaluation degraded: "
                f"{response.degraded_reason}"
            )
        ids = list(
            dict.fromkeys(
                result.document_id
                for result in response.results
            )
        )
        relevant_ranks = [
            index for index, item_id in enumerate(ids, 1)
            if item_id in case.relevant_document_ids
        ]
        recalled = len(set(ids) & case.relevant_document_ids)
        recalls.append(recalled / len(case.relevant_document_ids))
        reciprocals.append(1 / relevant_ranks[0] if relevant_ranks else 0.0)
        actual = sum(1 / math.log2(rank + 1) for rank in relevant_ranks)
        ideal_count = min(len(case.relevant_document_ids), cutoff)
        ideal = sum(
            1 / math.log2(rank + 1)
            for rank in range(1, ideal_count + 1)
        )
        discounts.append(actual / ideal)
        for result in response.results:
            result_count += 1
            leaked += int(result.project_id != project_id)
            candidate_leakage += int(
                result.governance_state
                not in {"approved", "verified"}
            )
            valid_citations += int(
                _citation_valid(
                    result,
                    snapshot,
                    context,
                    case.expected_citations,
                )
            )
        matches = {
            gold.chunk_id
            for gold in case.expected_citations
            if gold.document_id in case.relevant_document_ids
            and any(
                _citation_valid(
                    result,
                    snapshot,
                    context,
                    (gold,),
                )
                for result in response.results
            )
        }
        matched_gold += len(matches)
        expected_gold += max(
            len(case.relevant_document_ids),
            len(case.expected_citations),
        )
    divisor = len(cases)
    corpus_fingerprint = _fingerprint(sorted(
        (
            item.project_id, item.document_id, item.version_id, item.chunk_id,
            item.parent_id, item.source, item.locator, item.governance_state,
            hashlib.sha256(
                item.content.encode("utf-8")
            ).hexdigest(),
            item.content_hash,
        )
        for item in snapshot
    ))
    snapshot_fingerprint = _fingerprint([
        snapshot[0].project_id,
        snapshot[0].snapshot_id,
        snapshot[0].generation,
        corpus_fingerprint,
    ])
    adapter_config = None
    if dense_adapter:
        adapter_config = _adapter_fingerprint(dense_adapter)
    config_fingerprint = _fingerprint({
        "adapter": adapter_config,
        "dense_weight": 0.0 if mode == "lexical" else 1.0,
        "lexical": "bm25:k1=1.5:b=0.75:v1",
        "lexical_weight": 0.0 if mode == "dense" else 1.0,
        "limit": cutoff,
        "mode": mode,
        "rrf_k": 60,
    })
    evaluation_fingerprint = _fingerprint({
        "cases": _canonical_cases(cases),
        "config": config_fingerprint,
        "corpus": corpus_fingerprint,
        "snapshot": snapshot_fingerprint,
    })
    return EvaluationMetrics(
        recall_at_k=sum(recalls) / divisor,
        mrr=sum(reciprocals) / divisor,
        ndcg_at_k=sum(discounts) / divisor,
        citation_completeness=(
            matched_gold / expected_gold
            if expected_gold
            else 0.0
        ),
        citation_validity=(
            valid_citations / result_count
            if result_count
            else 1.0
        ),
        project_leakage=leaked / result_count if result_count else 0.0,
        candidate_leakage_count=candidate_leakage,
        query_count=divisor,
        corpus_fingerprint=corpus_fingerprint,
        snapshot_fingerprint=snapshot_fingerprint,
        config_fingerprint=config_fingerprint,
        evaluation_fingerprint=evaluation_fingerprint,
    )


def compare_retrieval_modes(
    cases: tuple[EvaluationCase, ...],
    corpus: tuple[SnapshotChunk, ...],
    dense_adapter: DenseAdapter,
    *,
    context: RetrievalContext,
    k: int = 5,
) -> dict[str, EvaluationMetrics]:
    """Return comparable lexical, dense, and hybrid reports."""
    return {
        mode: evaluate_retrieval(
            cases, corpus, context=context, mode=mode,
            dense_adapter=dense_adapter, k=k,
        )
        for mode in ("lexical", "dense", "hybrid")
    }


def assert_regression_thresholds(
    metrics: EvaluationMetrics, thresholds: Mapping[str, float]
) -> None:
    """Raise when metrics violate explicit offline regression thresholds."""
    supported = {
        "recall_at_k", "mrr", "ndcg_at_k", "citation_completeness",
        "citation_validity", "project_leakage", "candidate_leakage_count",
    }
    for name, threshold in thresholds.items():
        invalid_name = name not in supported
        invalid_type = isinstance(threshold, bool) or not isinstance(
            threshold,
            (int, float),
        )
        if invalid_name or invalid_type:
            raise ValueError("threshold is invalid")
        if not math.isfinite(threshold) or threshold < 0:
            raise ValueError("threshold is invalid")
        actual = float(getattr(metrics, name))
        upper_bound = name in {"project_leakage", "candidate_leakage_count"}
        passed = actual <= threshold if upper_bound else actual >= threshold
        if not passed:
            raise AssertionError(
                f"{name} regression: actual={actual}, "
                f"threshold={threshold}"
            )


def _validate_cases(cases: object) -> None:
    if not isinstance(cases, tuple) or not cases:
        raise ValueError("evaluation cases must be a non-empty tuple")
    for case in cases:
        if not isinstance(case, EvaluationCase):
            raise TypeError("evaluation case is invalid")
        validate_query(case.query)
        if not case.relevant_document_ids or any(
            not isinstance(item, str) or not item.strip()
            for item in case.relevant_document_ids
        ):
            raise ValueError("relevant document ids must be non-empty")
        if not isinstance(case.expected_citations, tuple):
            raise TypeError("expected citations must be a tuple")
        for citation in case.expected_citations:
            if not isinstance(citation, ExpectedCitation):
                raise TypeError("expected citation is invalid")


def _fingerprint(value: object) -> str:
    encoded = json.dumps(
        value,
        ensure_ascii=True,
        separators=(",", ":"),
        sort_keys=True,
    )
    return hashlib.sha256(encoded.encode("utf-8")).hexdigest()


def _adapter_fingerprint(adapter: DenseAdapter) -> str:
    value = adapter.config_fingerprint()
    if not isinstance(value, str) or not value.strip() or len(value) > 512:
        raise ValueError("adapter config fingerprint is invalid")
    return value


def _canonical_cases(
    cases: tuple[EvaluationCase, ...],
) -> list[dict[str, object]]:
    canonical: list[dict[str, object]] = []
    for case in cases:
        relevant = sorted(case.relevant_document_ids)
        citations = sorted(
            (
                item.project_id,
                item.snapshot_id,
                item.generation,
                item.document_id,
                item.version_id,
                item.chunk_id,
                item.parent_id,
                item.governance_state,
                item.source,
                item.locator,
                item.content_hash,
                item.snippet,
            )
            for item in case.expected_citations
        )
        canonical.append({
            "expected_citations": citations,
            "query": case.query,
            "relevant_document_ids": relevant,
        })
    return canonical
