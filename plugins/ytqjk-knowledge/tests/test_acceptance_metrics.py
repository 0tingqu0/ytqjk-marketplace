from __future__ import annotations

import copy
import sys
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.acceptance_metrics import (  # noqa: E402
    AcceptanceValidationError,
    EvidenceSample,
    LOW_CONFIDENCE_CUTOFF,
    MetricAlgorithms,
    NotConfiguredError,
    compute_metrics,
)


FACETS = frozenset(
    {
        "native", "scan", "mixed", "table", "image", "corrupt",
        "clear", "noisy", "tilted",
    }
)


def table() -> dict[str, object]:
    return {
        "structure": "<table><tr><td></td></tr></table>",
        "cells": [{"id": "c1", "text": "value"}],
    }


def truth_page(
    number: int, facets: list[str], text: str
) -> dict[str, object]:
    rich = number == 1
    return {
        "number": number,
        "facets": facets,
        "route": "NATIVE_TEXT" if rich else "OCR",
        "text": text,
        "reading_order": ["b1", "b2"] if rich else [],
        "table": table() if rich else None,
        "boxes": (
            [{"id": "b1", "xyxy": [0, 0, 10, 10]}] if rich else []
        ),
        "image_class": "diagram" if rich else None,
        "review_required": rich,
    }


def result_page(
    number: int, text: str, *, confidence: float = 0.9
) -> dict[str, object]:
    rich = number == 1
    return {
        "number": number,
        "route": "NATIVE_TEXT" if rich else "OCR",
        "text": text,
        "reading_order": ["b1", "b2"] if rich else [],
        "table": table() if rich else None,
        "boxes": (
            [{"id": "b1", "xyxy": [0, 0, 10, 10]}] if rich else []
        ),
        "image_top3": ["diagram", "photo"] if rich else [],
        "confidence": confidence,
        "reviewed": confidence < LOW_CONFIDENCE_CUTOFF,
    }


def sample() -> EvidenceSample:
    truth = {
        "schema_version": 1,
        "sample_id": "gold-1",
        "source_sha256": "a" * 64,
        "pages": [
            truth_page(1, ["native", "table", "image"], "native"),
            truth_page(2, ["scan", "clear", "tilted"], "clear"),
            truth_page(3, ["scan", "noisy"], "noisy"),
        ],
        "retry_ids": ["retry-1"],
        "conflict_ids": ["conflict-1"],
        "retrieval_queries": [
            {"id": "q1", "relevant_document_ids": ["doc-1"]}
        ],
        "expected_status": "ACCEPTED",
        "expected_error_category": None,
    }
    result = {
        "schema_version": 1,
        "sample_id": "gold-1",
        "source_sha256": "a" * 64,
        "sidecar_sha256": "b" * 64,
        "pages": [
            result_page(1, "native", confidence=0.1),
            result_page(2, "clear"),
            result_page(3, "noisy"),
        ],
        "retry_events": [{"id": "retry-1", "succeeded": True}],
        "conflict_events": [{"id": "conflict-1", "reviewed": True}],
        "retrieval_results": [
            {
                "id": "q1",
                "top5": [
                    {"document_id": "doc-1", "governance": "approved"}
                ],
            }
        ],
        "warm_seconds": [10, 20],
        "status": "ACCEPTED",
        "error": None,
    }
    return EvidenceSample(
        "gold-1", FACETS - {"corrupt"}, 3,
        "a" * 64, "b" * 64, truth, result,
    )


def corrupt_sample() -> EvidenceSample:
    truth = {
        "schema_version": 1, "sample_id": "corrupt-1",
        "source_sha256": "c" * 64, "pages": [], "retry_ids": [],
        "conflict_ids": [], "retrieval_queries": [],
        "expected_status": "REJECTED",
        "expected_error_category": "CORRUPT_DOCUMENT",
    }
    result = {
        "schema_version": 1, "sample_id": "corrupt-1",
        "source_sha256": "c" * 64, "sidecar_sha256": "d" * 64,
        "pages": [], "retry_events": [], "conflict_events": [],
        "retrieval_results": [], "warm_seconds": [1],
        "status": "REJECTED",
        "error": {"category": "CORRUPT_DOCUMENT", "ref": "e" * 64},
    }
    return EvidenceSample(
        "corrupt-1", frozenset({"corrupt"}), 0,
        "c" * 64, "d" * 64, truth, result,
    )


def evidence(
    primary: EvidenceSample | None = None,
) -> tuple[EvidenceSample, ...]:
    return (primary or sample(), corrupt_sample())


def algorithms(score: float = 1.0) -> MetricAlgorithms:
    return MetricAlgorithms(
        "test-teds-v1",
        lambda expected, actual: score if expected and actual else 0.0,
    )


def test_exact_observations_compute_passing_metrics() -> None:
    metrics = compute_metrics(evidence(), algorithms())
    assert all(
        metrics[name] == 0
        for name in (
            "native_cer", "clear_scan_cer", "noisy_scan_cer",
            "missing_page_count", "duplicate_page_count",
            "candidate_leakage_count",
        )
    )
    assert metrics["image_bbox_iou_p05"] == 1
    assert metrics["corrupt_rejection_accuracy"] == 1
    assert LOW_CONFIDENCE_CUTOFF == 0.88


def test_metrics_are_derived_from_observations() -> None:
    evidence = copy.deepcopy(sample())
    evidence.result["pages"][2]["text"] = "noise"
    evidence.result["pages"][0]["reviewed"] = False
    evidence.result["conflict_events"][0]["reviewed"] = False
    evidence.result["retrieval_results"][0]["top5"][0][
        "governance"
    ] = "candidate"
    metrics = compute_metrics(
        (evidence, corrupt_sample()), algorithms()
    )
    assert metrics["noisy_scan_cer"] == pytest.approx(1 / 5)
    assert metrics["review_trigger_recall"] == 0
    assert metrics["conflict_review_rate"] == 0
    assert metrics["candidate_leakage_count"] == 1


def test_routes_order_tables_images_retrieval_and_timings_are_derived(
) -> None:
    evidence = copy.deepcopy(sample())
    page = evidence.result["pages"][0]
    page["route"] = "ocr"
    page["reading_order"] = ["b2", "b1"]
    page["table"]["structure"] = "different"
    page["table"]["cells"][0]["text"] = "wrong"
    page["image_top3"] = ["photo"]
    evidence.result["retry_events"][0]["succeeded"] = False
    evidence.result["retrieval_results"][0]["top5"] = []
    evidence.result["warm_seconds"] = [10, 30]
    metrics = compute_metrics(
        (evidence, corrupt_sample()), algorithms(0.5)
    )
    assert metrics["routing_accuracy"] == pytest.approx(2 / 3)
    assert metrics["reading_order_kendall"] == -1
    assert metrics["table_teds"] == 0.5
    assert metrics["table_cell_accuracy"] == 0
    assert metrics["retry_success_rate"] == 0
    assert metrics["image_macro_f1"] == 0
    assert metrics["image_top_3_recall"] == 0
    assert metrics["retrieval_recall_at_5"] == 0
    assert metrics["warm_mean_seconds"] == pytest.approx(41 / 3)
    assert metrics["warm_median_seconds"] == 10
    assert metrics["warm_p95_seconds"] == 28
    assert metrics["native_p95_seconds"] == 29


def test_missing_and_duplicate_pages_are_counted() -> None:
    evidence = copy.deepcopy(sample())
    evidence.result["pages"] = [
        evidence.result["pages"][0],
        copy.deepcopy(evidence.result["pages"][0]),
        evidence.result["pages"][2],
    ]
    metrics = compute_metrics(
        (evidence, corrupt_sample()), algorithms()
    )
    assert metrics["missing_page_count"] == 1
    assert metrics["duplicate_page_count"] == 1


def test_bbox_p05_uses_lower_tail_not_p95_best_case() -> None:
    evidence = copy.deepcopy(sample())
    evidence.result["pages"][0]["boxes"][0]["xyxy"] = [0, 0, 5, 10]
    metrics = compute_metrics(
        (evidence, corrupt_sample()), algorithms()
    )
    assert metrics["image_bbox_iou_median"] == 0.5
    assert metrics["image_bbox_iou_p05"] == 0.5


def test_teds_requires_named_adapter_and_valid_score() -> None:
    with pytest.raises(NotConfiguredError, match="TEDS adapter"):
        compute_metrics(evidence(), MetricAlgorithms())
    with pytest.raises(AcceptanceValidationError, match="TEDS"):
        compute_metrics(evidence(), algorithms(float("nan")))


def test_absent_measured_stratum_blocks_instead_of_defaulting() -> None:
    evidence = copy.deepcopy(sample())
    evidence.truth["pages"][0]["review_required"] = False
    evidence.result["pages"][0]["confidence"] = 0.9
    with pytest.raises(NotConfiguredError, match="review triggers"):
        compute_metrics((evidence, corrupt_sample()), algorithms())


def test_cer_work_is_bounded_with_validation_error() -> None:
    evidence = copy.deepcopy(sample())
    evidence.truth["pages"][0]["text"] = "a" * 2_001
    evidence.result["pages"][0]["text"] = "a" * 2_001
    with pytest.raises(AcceptanceValidationError, match="CER input"):
        compute_metrics((evidence, corrupt_sample()), algorithms())


def test_truth_review_target_cannot_be_evaded_by_high_confidence() -> None:
    primary = copy.deepcopy(sample())
    primary.result["pages"][0]["confidence"] = 0.99
    primary.result["pages"][0]["reviewed"] = False
    metrics = compute_metrics(evidence(primary), algorithms())
    assert metrics["review_trigger_recall"] == 0


def test_corrupt_rejection_uses_truth_status_and_error_category() -> None:
    corrupt = copy.deepcopy(corrupt_sample())
    corrupt.result["status"] = "ACCEPTED"
    corrupt.result["error"] = None
    metrics = compute_metrics((sample(), corrupt), algorithms())
    assert metrics["corrupt_rejection_accuracy"] == 0
