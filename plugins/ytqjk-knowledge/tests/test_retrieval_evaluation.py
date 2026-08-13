from __future__ import annotations

import hashlib
import math
import sys
from dataclasses import replace
from pathlib import Path
from typing import Any

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.dense_search import DenseMatch  # noqa: E402
from scripts.evaluation import (  # noqa: E402
    EvaluationCase,
    EvaluationMetrics,
    ExpectedCitation,
    _citation_valid,
    assert_regression_thresholds,
    compare_retrieval_modes,
    evaluate_retrieval,
)
from scripts.hybrid_search import hybrid_search  # noqa: E402
from scripts.retrieval_contracts import (  # noqa: E402
    RetrievalContext,
    SnapshotChunk,
)


PROJECT_ID = "00000000-0000-0000-0000-000000000001"
SNAPSHOT_ID = "00000000-0000-0000-0000-000000000002"
CONTEXT = RetrievalContext(PROJECT_ID, SNAPSHOT_ID, 1)


def item(chunk_id: str, document_id: str, text: str) -> SnapshotChunk:
    parent = f"Parent: {text}."
    return SnapshotChunk(
        PROJECT_ID, SNAPSHOT_ID, 1, document_id, f"version-{document_id}",
        chunk_id, f"parent-{document_id}", "fixture",
        f"{document_id}.md", "verified", "ACTIVE", True, False,
        text, parent,
        hashlib.sha256(parent.encode()).hexdigest(),
    )


CORPUS = (
    item("c1", "odometry", "wheel encoder calibration"),
    item("c2", "camera", "camera exposure setting"),
    item("c3", "network", "network timeout recovery"),
)


def expected(
    chunk_id: str,
    document_id: str,
    snippet: str,
) -> ExpectedCitation:
    parent = f"Parent: {snippet}."
    return ExpectedCitation(
        PROJECT_ID, SNAPSHOT_ID, 1, document_id, f"version-{document_id}",
        chunk_id, f"parent-{document_id}", "verified", "fixture",
        f"{document_id}.md",
        hashlib.sha256(parent.encode()).hexdigest(), snippet,
    )


CASES = (
    EvaluationCase(
        "wheel calibration",
        frozenset({"odometry"}),
        (expected("c1", "odometry", "wheel encoder calibration"),),
    ),
    EvaluationCase(
        "camera exposure",
        frozenset({"camera"}),
        (expected("c2", "camera", "camera exposure setting"),),
    ),
)


def evaluate(
    cases: tuple[EvaluationCase, ...] = CASES,
    **kwargs: Any,
) -> EvaluationMetrics:
    return evaluate_retrieval(
        cases,
        CORPUS,
        context=CONTEXT,
        **kwargs,
    )


def dense_metrics(config: str) -> EvaluationMetrics:
    return evaluate(
        mode="dense",
        dense_adapter=DenseAdapter(config),
        k=2,
    )


class DenseAdapter:
    def __init__(self, config: str = "model-v1:index-v1") -> None:
        self._config = config

    def search(
        self, query: str, chunks: tuple[SnapshotChunk, ...], limit: int
    ) -> tuple[DenseMatch, ...]:
        del chunks, limit
        target = "c1" if "wheel" in query else "c2"
        return (DenseMatch(target, 1.0),)

    def config_fingerprint(self) -> str:
        return self._config


def test_offline_metrics_are_deterministic_and_complete() -> None:
    options = {"mode": "hybrid", "dense_adapter": DenseAdapter(), "k": 2}
    first = evaluate(**options)
    second = evaluate(**options)
    assert first == second
    assert (
        first.recall_at_k,
        first.mrr,
        first.ndcg_at_k,
        first.citation_completeness,
        first.citation_validity,
        first.project_leakage,
        first.candidate_leakage_count,
    ) == (1.0, 1.0, 1.0, 1.0, 1.0, 0.0, 0)


def test_lexical_dense_and_hybrid_comparison() -> None:
    report = compare_retrieval_modes(
        CASES, CORPUS, DenseAdapter(), context=CONTEXT, k=2
    )
    assert set(report) == {"lexical", "dense", "hybrid"}
    assert len({report[mode].config_fingerprint for mode in report}) == 3


def test_metrics_match_hand_calculation_for_rank_two() -> None:
    case = (
        EvaluationCase(
            "camera exposure wheel",
            frozenset({"odometry"}),
            CASES[0].expected_citations,
        ),
    )
    metrics = evaluate(case, mode="lexical", k=2)
    assert metrics.recall_at_k == 1.0
    assert metrics.mrr == 0.5
    assert metrics.ndcg_at_k == pytest.approx(1 / math.log2(3))


def test_regression_thresholds_fail_closed() -> None:
    metrics = evaluate(mode="lexical", k=1)
    assert_regression_thresholds(
        metrics,
        {
            "recall_at_k": 1.0,
            "project_leakage": 0.0,
            "candidate_leakage_count": 0.0,
        },
    )
    with pytest.raises(AssertionError, match="mrr"):
        assert_regression_thresholds(metrics, {"mrr": 1.1})
    with pytest.raises(ValueError, match="threshold"):
        assert_regression_thresholds(metrics, {"unknown": 0.5})
    leaked = replace(metrics, project_leakage=1.0, candidate_leakage_count=1)
    for name in ("project_leakage", "candidate_leakage_count"):
        with pytest.raises(AssertionError, match=name):
            assert_regression_thresholds(leaked, {name: 0.0})


def test_evaluation_rejects_empty_gold_and_cross_project_corpus() -> None:
    with pytest.raises(ValueError, match="relevant"):
        evaluate_retrieval(
            (EvaluationCase("query", frozenset(), ()),),
            CORPUS,
            context=CONTEXT,
            k=1,
        )
    leaked = item("c4", "other", "query")
    leaked = replace(leaked, project_id="project-b")
    with pytest.raises(ValueError, match="project"):
        evaluate_retrieval(CASES, CORPUS + (leaked,), context=CONTEXT, k=1)
    candidate = replace(CORPUS[0], governance_state="candidate")
    with pytest.raises(ValueError, match="governance"):
        evaluate_retrieval(CASES, (candidate,), context=CONTEXT, k=1)


def test_citation_validity_uses_trusted_corpus_not_result_self_proof() -> None:
    response = hybrid_search("wheel", CORPUS, context=CONTEXT, limit=1)
    valid = response.results[0]
    forged = replace(
        valid,
        project_id="00000000-0000-0000-0000-000000000099",
        snapshot_id="00000000-0000-0000-0000-000000000098",
        generation=99,
        document_id="forged-document",
        version_id="forged-version",
        chunk_id="forged-chunk",
        parent_id="forged-parent",
        governance_state="candidate",
        source="forged-source",
        locator="forged-locator",
        content_hash=hashlib.sha256(b"forged").hexdigest(),
        content="forged",
        matched_content="forged",
        fragment_locator="forged-locator#chunk=forged-chunk",
    )
    gold = CASES[0].expected_citations
    assert not _citation_valid(forged, CORPUS, CONTEXT, gold)


def test_config_fingerprint_includes_stable_adapter_configuration() -> None:
    first, second, repeat = (
        dense_metrics(config)
        for config in (
            "model-a:index-1",
            "model-a:index-2",
            "model-a:index-1",
        )
    )
    assert first.config_fingerprint != second.config_fingerprint
    assert first.config_fingerprint == repeat.config_fingerprint

    class MissingConfig:
        def search(
            self, query: str, chunks: tuple[SnapshotChunk, ...], limit: int
        ) -> tuple[DenseMatch, ...]:
            return DenseAdapter().search(query, chunks, limit)

    options = {"mode": "dense", "dense_adapter": MissingConfig(), "k": 2}
    with pytest.raises(AttributeError, match="config_fingerprint"):
        evaluate(**options)


def test_parent_deduplication_precedes_child_truncation() -> None:
    shared = item("a000", "shared", "target")
    children = tuple(
        replace(shared, chunk_id=f"a{index:03d}")
        for index in range(100)
    )
    second = item("z101", "second", "target extra words make lower score")
    response = hybrid_search(
        "target",
        children + (second,),
        context=CONTEXT,
        limit=100,
    )
    assert [item.document_id for item in response.results] == [
        "shared",
        "second",
    ]
    first, second_result = response.results
    assert first.scores.lexical > second_result.scores.lexical
    assert first.scores.rrf > second_result.scores.rrf


def test_dense_oserror_degrades_but_security_and_data_errors_block() -> None:
    class FailingDense:
        def __init__(self, error: Exception) -> None:
            self._error = error

        def search(
            self, query: str, chunks: tuple[SnapshotChunk, ...], limit: int
        ) -> tuple[DenseMatch, ...]:
            raise self._error

    degraded = hybrid_search(
        "wheel",
        CORPUS,
        context=CONTEXT,
        dense_adapter=FailingDense(OSError("local index corrupt")),
    )
    assert degraded.status == "DEGRADED_LEXICAL_ONLY"
    assert degraded.dense_status == "ERROR"
    assert "local index corrupt" in (degraded.degraded_reason or "")
    with pytest.raises(PermissionError):
        hybrid_search(
            "wheel",
            CORPUS,
            context=CONTEXT,
            dense_adapter=FailingDense(PermissionError("denied")),
        )
    with pytest.raises(ValueError, match="unsafe data"):
        hybrid_search(
            "wheel",
            CORPUS,
            context=CONTEXT,
            dense_adapter=FailingDense(ValueError("unsafe data")),
        )


@pytest.mark.parametrize(
    "changed",
    [
        replace(CASES[0], query="wheel encoder calibration revised"),
        replace(
            CASES[0],
            expected_citations=(
                replace(
                    CASES[0].expected_citations[0],
                    locator="changed-locator.md",
                ),
            ),
        ),
    ],
)
def test_evaluation_fingerprint_changes_with_query_or_gold(
    changed: EvaluationCase,
) -> None:
    baseline = evaluate(mode="lexical", k=2)
    metrics = evaluate((changed, CASES[1]), mode="lexical", k=2)
    assert (
        baseline.evaluation_fingerprint
        != metrics.evaluation_fingerprint
    )


@pytest.mark.parametrize(
    "gold",
    [
        (),
        (
            replace(
                CASES[0].expected_citations[0],
                content_hash="0" * 64,
            ),
        ),
    ],
)
def test_missing_or_wrong_expected_citation_reduces_metrics(
    gold: tuple[ExpectedCitation, ...],
) -> None:
    case = replace(CASES[0], expected_citations=gold)
    metrics = evaluate((case,), mode="lexical", k=2)
    assert (
        metrics.citation_completeness,
        metrics.citation_validity,
    ) == (0, 0)
    with pytest.raises(AssertionError, match="citation_completeness"):
        assert_regression_thresholds(
            metrics,
            {"citation_completeness": 1.0},
        )
