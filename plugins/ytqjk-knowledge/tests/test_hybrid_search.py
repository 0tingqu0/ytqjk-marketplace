from __future__ import annotations

import hashlib
import sys
import time
from dataclasses import replace
from pathlib import Path
from typing import Any

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.dense_search import (  # noqa: E402
    DenseMatch,
    DenseUnavailableError,
)
from scripts.hybrid_search import hybrid_search  # noqa: E402
from scripts.lexical_search import lexical_search  # noqa: E402
from scripts.retrieval_contracts import (  # noqa: E402
    RetrievalContext,
    SearchResponse,
    SnapshotChunk,
)


PROJECT_ID = "00000000-0000-0000-0000-000000000001"
SNAPSHOT_ID = "00000000-0000-0000-0000-000000000002"
CONTEXT = RetrievalContext(PROJECT_ID, SNAPSHOT_ID, 1)
Corpus = tuple[SnapshotChunk, ...]


def search(
    corpus: Corpus,
    query: str = "query",
    **kwargs: Any,
) -> SearchResponse:
    return hybrid_search(
        query,
        corpus,
        context=CONTEXT,
        **kwargs,
    )


def chunk(
    chunk_id: str,
    document_id: str,
    content: str,
    parent_content: str,
    **changes: Any,
) -> SnapshotChunk:
    digest = hashlib.sha256(parent_content.encode()).hexdigest()
    values = {
        "project_id": PROJECT_ID,
        "snapshot_id": SNAPSHOT_ID,
        "generation": 1,
        "document_id": document_id,
        "version_id": f"version-{document_id}",
        "chunk_id": chunk_id,
        "parent_id": f"parent-{document_id}",
        "source": "fixture",
        "locator": f"notes/{document_id}.md#L1",
        "governance_state": "approved",
        "snapshot_state": "ACTIVE",
        "snapshot_member": True,
        "soft_deleted": False,
        "content": content,
        "parent_content": parent_content,
        "content_hash": digest,
    }
    values.update(changes)
    return SnapshotChunk(**values)


@pytest.fixture
def corpus() -> Corpus:
    parent = "wheel odometry calibration; encoder scale procedure."
    return (
        chunk("chunk-a1", "a", "wheel odometry calibration", parent),
        chunk("chunk-a2", "a", "encoder scale", parent),
        chunk(
            "chunk-b1",
            "b",
            "camera exposure",
            "camera exposure troubleshooting.",
        ),
    )


class DenseStub:
    def __init__(
        self,
        matches: tuple[DenseMatch, ...] = (),
        error: Exception | None = None,
    ) -> None:
        self.matches = matches
        self.error = error

    def search(
        self,
        query: str,
        chunks: Corpus,
        limit: int,
    ) -> tuple[DenseMatch, ...]:
        del query, chunks, limit
        if self.error:
            raise self.error
        return self.matches

    def config_fingerprint(self) -> str:
        return "model-v1:index-v1"


class RerankerStub:
    def __init__(
        self,
        mode: str,
        delay: float = 0,
        events: list[str] | None = None,
    ) -> None:
        self.mode = mode
        self.delay = delay
        self.events = events

    def rerank(
        self,
        query: str,
        candidates: tuple[object, ...],
    ) -> tuple[str, ...]:
        del query
        time.sleep(self.delay)
        if self.events is not None:
            self.events.append("finished")
        if self.mode == "boom":
            raise RuntimeError("reranker boom")
        if self.mode == "unknown":
            return ("unknown-parent",)
        ids = tuple(item.parent_id for item in candidates)
        return tuple(reversed(ids)) if self.mode == "reverse" else ids


ADAPTER = DenseStub(
    (
        DenseMatch("chunk-b1", 0.9),
        DenseMatch("chunk-a1", 0.8),
    )
)


def test_lexical_returns_cited_parent(corpus: Corpus) -> None:
    result = search(corpus, "encoder scale", limit=2).results[0]
    assert result.document_id == "a"
    assert result.chunk_id == "chunk-a2"
    assert result.matched_content == "encoder scale"
    assert result.fragment_locator.endswith("#chunk=chunk-a2")
    assert result.governance_state == "approved"
    assert result.scores.lexical > 0
    assert result.content_hash == hashlib.sha256(
        result.content.encode()
    ).hexdigest()


def test_hybrid_is_deterministic_with_score_breakdown(
    corpus: Corpus,
) -> None:
    options = {"limit": 2, "dense_adapter": ADAPTER}
    first = search(corpus, "wheel calibration", **options)
    assert first == search(corpus, "wheel calibration", **options)
    assert first.mode == "hybrid"
    assert first.status == first.dense_status == "OK"
    assert [item.document_id for item in first.results] == ["a", "b"]
    assert first.results[0].scores.lexical > 0
    assert first.results[0].scores.dense > 0
    assert first.results[0].scores.rrf > 0


def test_dense_failure_explicitly_degrades(corpus: Corpus) -> None:
    broken = DenseStub(error=DenseUnavailableError("model unavailable"))
    response = search(corpus, "camera", dense_adapter=broken)
    assert response.status == "DEGRADED_LEXICAL_ONLY"
    assert response.dense_status == "ERROR"
    assert "model unavailable" in (response.degraded_reason or "")
    assert response.results[0].document_id == "b"
    dense_only = search(
        corpus,
        "camera",
        dense_adapter=broken,
        lexical_weight=0,
    )
    assert dense_only.results[0].document_id == "b"


def test_reranker_reorders_and_timeout_has_no_background_work(
    corpus: Corpus,
) -> None:
    baseline = search(corpus, "wheel camera", limit=2)
    reranked = search(
        corpus,
        "wheel camera",
        limit=2,
        reranker=RerankerStub("reverse"),
    )
    expected = [item.parent_id for item in reversed(baseline.results)]
    assert [item.parent_id for item in reranked.results] == expected
    events: list[str] = []
    started = time.monotonic()
    timed_out = search(
        corpus,
        "wheel camera",
        limit=2,
        reranker=RerankerStub("same", 0.03, events),
        rerank_timeout=0.001,
    )
    assert time.monotonic() >= started
    assert timed_out.reranker_status == "TIMEOUT"
    assert timed_out.results == baseline.results
    assert events == ["finished"]


def test_reranker_error_and_dense_reason_are_preserved(
    corpus: Corpus,
) -> None:
    missing = DenseStub(error=DenseUnavailableError("dense missing"))
    response = search(
        corpus,
        "wheel",
        dense_adapter=missing,
        reranker=RerankerStub("boom"),
    )
    assert response.reranker_status == "ERROR"
    assert "dense missing" in (response.degraded_reason or "")
    assert "reranker boom" in (response.degraded_reason or "")
    with pytest.raises(ValueError, match="every candidate"):
        search(corpus, "wheel", reranker=RerankerStub("unknown"))


@pytest.mark.parametrize(
    ("changes", "message"),
    [
        ({"project_id": "other"}, "project"),
        ({"snapshot_id": "other"}, "snapshot"),
        ({"generation": 2}, "generation"),
        ({"content_hash": "0" * 64}, "hash"),
        ({"content": "absent"}, "parent"),
        ({"governance_state": "candidate"}, "governance"),
        ({"snapshot_state": "BUILDING"}, "snapshot state"),
        ({"soft_deleted": True}, "soft-deleted"),
        ({"snapshot_member": False}, "snapshot member"),
    ],
)
def test_invalid_snapshot_data_blocks(
    changes: dict[str, object],
    message: str,
    corpus: Corpus,
) -> None:
    with pytest.raises(ValueError, match=message):
        search((replace(corpus[0], **changes),))


@pytest.mark.parametrize(
    "context",
    [
        RetrievalContext("not-a-uuid", SNAPSHOT_ID, 1),
        RetrievalContext(PROJECT_ID, "not-a-uuid", 1),
        RetrievalContext(
            "AAAAAAAA-0000-0000-0000-000000000001",
            SNAPSHOT_ID,
            1,
        ),
    ],
)
def test_trusted_context_requires_canonical_uuids(
    context: RetrievalContext,
    corpus: Corpus,
) -> None:
    with pytest.raises(ValueError, match="UUID"):
        hybrid_search("wheel", corpus, context=context)


def test_trusted_context_cannot_be_self_proved(corpus: Corpus) -> None:
    foreign = replace(
        CONTEXT,
        project_uuid="00000000-0000-0000-0000-000000000099",
    )
    with pytest.raises(ValueError, match="trusted context"):
        hybrid_search("wheel", corpus, context=foreign)


@pytest.mark.parametrize(
    ("query", "kwargs", "message"),
    [
        ("", {}, "query"),
        ("q" * 4097, {}, "query exceeds"),
        ("q", {"limit": 0}, "limit"),
        ("q", {"limit": 101}, "limit exceeds"),
        ("q", {"lexical_weight": float("nan")}, "weight"),
        ("q", {"dense_weight": float("inf")}, "weight"),
        ("q", {"rrf_k": 0}, "rrf_k"),
        ("q", {"rerank_timeout": float("nan")}, "timeout"),
    ],
)
def test_public_parameters_are_strict(
    query: str,
    kwargs: dict[str, object],
    message: str,
    corpus: Corpus,
) -> None:
    with pytest.raises((TypeError, ValueError), match=message):
        search(corpus, query, **kwargs)


def test_duplicate_or_unknown_ids_block(corpus: Corpus) -> None:
    with pytest.raises(ValueError, match="duplicate chunk"):
        search(corpus + (corpus[0],))
    duplicate = DenseStub(
        (
            DenseMatch("chunk-a1", 0.8),
            DenseMatch("chunk-a1", 0.7),
        )
    )
    with pytest.raises(ValueError, match="duplicate id"):
        search(corpus, dense_adapter=duplicate)
    unknown = DenseStub((DenseMatch("unknown", float("nan")),))
    with pytest.raises(ValueError, match="unknown chunk"):
        search(corpus, dense_adapter=unknown)


def test_lexical_api_rejects_empty_query(corpus: Corpus) -> None:
    with pytest.raises(ValueError, match="query"):
        lexical_search(" ", corpus, 2, context=CONTEXT)
