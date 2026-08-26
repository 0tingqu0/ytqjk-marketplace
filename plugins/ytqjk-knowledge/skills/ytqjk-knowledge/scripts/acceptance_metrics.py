"""Computed document-acceptance metrics over bound per-sample evidence."""

from __future__ import annotations

import math
import statistics
from dataclasses import dataclass
from typing import Callable, Mapping

from .acceptance_report import (
    FACETS,
    LOW_CONFIDENCE_CUTOFF,
    AcceptanceValidationError,
    require,
)

class NotConfiguredError(AcceptanceValidationError):
    pass


@dataclass(frozen=True, slots=True)
class MetricAlgorithms:
    teds_id: str | None = None
    teds: Callable[[str, str], float] | None = None


@dataclass(frozen=True, slots=True)
class EvidenceSample:
    sample_id: str
    facets: frozenset[str]
    pages: int
    source_sha256: str
    sidecar_sha256: str
    truth: Mapping[str, object]
    result: Mapping[str, object]


def compute_metrics(
    samples: tuple[EvidenceSample, ...],
    algorithms: MetricAlgorithms,
) -> dict[str, float]:
    if not isinstance(samples, tuple) or not samples:
        raise AcceptanceValidationError("evidence samples must be non-empty")
    if not algorithms.teds_id or not callable(algorithms.teds):
        raise NotConfiguredError("NOT_CONFIGURED: TEDS adapter")
    if any(not isinstance(sample, EvidenceSample) for sample in samples):
        raise AcceptanceValidationError("evidence sample is invalid")
    cer = {name: [0, 0] for name in ("native", "clear", "noisy")}
    route_ok = route_total = missing = duplicate = 0
    orders: list[float] = []
    teds: list[float] = []
    cell_ok = cell_total = 0
    ious: list[float] = []
    review_ok = review_total = retry_ok = retry_total = 0
    conflict_ok = conflict_total = leakage = 0
    corrupt_ok = corrupt_total = 0
    truth_labels: list[str] = []
    predictions: list[str] = []
    top3_ok = 0
    recalls: list[float] = []
    warm: list[float] = []
    native_warm: list[float] = []
    for sample in samples:
        if "corrupt" in sample.facets:
            corrupt_total += 1
            result_error = sample.result["error"]
            correct = sample.result["status"] == sample.truth["expected_status"]
            correct = correct and result_error is not None
            if result_error is not None:
                correct = correct and result_error["category"] == sample.truth[
                    "expected_error_category"
                ]
            corrupt_ok += int(correct)
        truth_pages = {page["number"]: page for page in sample.truth["pages"]}
        result_groups: dict[int, list[Mapping[str, object]]] = {}
        for page in sample.result["pages"]:
            result_groups.setdefault(page["number"], []).append(page)
        missing += sum(number not in result_groups for number in truth_pages)
        duplicate += sum(
            max(0, len(group) - 1)
            if number in truth_pages else len(group)
            for number, group in result_groups.items()
        )
        for number, truth in truth_pages.items():
            result = result_groups.get(number, [None])[0]
            route_total += 1
            route_ok += int(result is not None and
                            result["route"] == truth["route"])
            _collect_page(cer, orders, teds, ious, truth, result, algorithms)
            matched, total = _cell_score(truth["table"], result)
            cell_ok += matched
            cell_total += total
            image_class = truth["image_class"]
            if image_class is not None:
                top3 = result["image_top3"] if result else []
                truth_labels.append(image_class)
                predictions.append(top3[0] if top3 else "<missing>")
                top3_ok += int(image_class in top3[:3])
            observed_low = (
                result is not None
                and result["confidence"] < LOW_CONFIDENCE_CUTOFF
            )
            if truth["review_required"] or observed_low:
                review_total += 1
                review_ok += int(result is not None and result["reviewed"])
        good, total = _events(
            sample.truth["retry_ids"], sample.result["retry_events"],
            "succeeded",
        )
        retry_ok += good
        retry_total += total
        good, total = _events(
            sample.truth["conflict_ids"], sample.result["conflict_events"],
            "reviewed",
        )
        conflict_ok += good
        conflict_total += total
        scores, leaked = _retrieval(sample.truth, sample.result)
        recalls.extend(scores)
        leakage += leaked
        timings = list(sample.result["warm_seconds"])
        warm.extend(timings)
        if "native" in sample.facets:
            native_warm.extend(timings)
    required = {
        "native CER": cer["native"][1],
        "clear scan CER": cer["clear"][1],
        "noisy scan CER": cer["noisy"][1],
        "reading order": len(orders), "tables": len(teds),
        "table cells": cell_total, "image boxes": len(ious),
        "review triggers": review_total, "retries": retry_total,
        "conflicts": conflict_total, "images": len(truth_labels),
        "corrupt samples": corrupt_total,
        "retrieval queries": len(recalls), "warm timings": len(warm),
        "native timings": len(native_warm),
    }
    absent = [name for name, count in required.items() if not count]
    if absent:
        raise NotConfiguredError("NOT_CONFIGURED: " + ", ".join(absent))
    return {
        "native_cer": cer["native"][0] / cer["native"][1],
        "clear_scan_cer": cer["clear"][0] / cer["clear"][1],
        "noisy_scan_cer": cer["noisy"][0] / cer["noisy"][1],
        "routing_accuracy": route_ok / route_total,
        "missing_page_count": float(missing),
        "duplicate_page_count": float(duplicate),
        "reading_order_kendall": statistics.mean(orders),
        "table_teds": statistics.mean(teds),
        "table_cell_accuracy": cell_ok / cell_total,
        "image_bbox_iou_median": statistics.median(ious),
        "image_bbox_iou_p05": _percentile(ious, 0.05),
        "retry_success_rate": retry_ok / retry_total,
        "conflict_review_rate": conflict_ok / conflict_total,
        "review_trigger_recall": review_ok / review_total,
        "corrupt_rejection_accuracy": corrupt_ok / corrupt_total,
        "image_macro_f1": _macro_f1(truth_labels, predictions),
        "image_top_3_recall": top3_ok / len(truth_labels),
        "retrieval_recall_at_5": statistics.mean(recalls),
        "candidate_leakage_count": float(leakage),
        "warm_mean_seconds": statistics.mean(warm),
        "warm_median_seconds": statistics.median(warm),
        "warm_p95_seconds": _percentile(warm, 0.95),
        "native_p95_seconds": _percentile(native_warm, 0.95),
    }


def _collect_page(
    cer: dict[str, list[int]], orders: list[float], teds: list[float],
    ious: list[float], truth: Mapping[str, object],
    result: Mapping[str, object] | None, algorithms: MetricAlgorithms,
) -> None:
    observed = result["text"] if result else ""
    for name, needed in (
        ("native", {"native"}), ("clear", {"scan", "clear"}),
        ("noisy", {"scan", "noisy"}),
    ):
        if needed <= set(truth["facets"]):
            cer[name][0] += _edit_distance(truth["text"], observed)
            cer[name][1] += len(truth["text"])
    order = truth["reading_order"]
    if len(order) >= 2:
        seen = result["reading_order"] if result else []
        orders.append(_kendall(order, seen))
    if truth["table"] is not None:
        score = 0.0 if not result or result["table"] is None else float(
            algorithms.teds(
                truth["table"]["structure"],
                result["table"]["structure"],
            )
        )
        if not math.isfinite(score) or not 0 <= score <= 1:
            raise AcceptanceValidationError("TEDS adapter score is invalid")
        teds.append(score)
    if truth["boxes"]:
        ious.extend(_box_ious(truth["boxes"], result))


def _cell_score(
    truth: object, result: Mapping[str, object] | None
) -> tuple[int, int]:
    if truth is None:
        return 0, 0
    observed = result["table"] if result else None
    left = {item["id"]: item["text"] for item in truth["cells"]}
    right = ({item["id"]: item["text"] for item in observed["cells"]}
             if observed else {})
    keys = set(left) | set(right)
    return sum(left.get(key) == right.get(key) for key in keys), len(keys)


def _events(ids: object, events: object, field: str) -> tuple[int, int]:
    expected = set(ids)
    observed = {item["id"]: item[field] for item in events}
    if not set(observed) <= expected:
        raise AcceptanceValidationError("unexpected event id")
    return sum(bool(observed.get(item)) for item in expected), len(expected)


def _retrieval(
    truth: Mapping[str, object], result: Mapping[str, object]
) -> tuple[list[float], int]:
    expected = {item["id"]: item for item in truth["retrieval_queries"]}
    observed = {item["id"]: item for item in result["retrieval_results"]}
    if not set(observed) <= set(expected):
        raise AcceptanceValidationError("unexpected retrieval query")
    scores, leakage = [], 0
    for query_id, query in expected.items():
        relevant = set(query["relevant_document_ids"])
        hits = observed.get(query_id, {}).get("top5", [])[:5]
        found = {item["document_id"] for item in hits}
        scores.append(len(found & relevant) / len(relevant))
        leakage += sum(item["governance"] == "candidate" for item in hits)
    return scores, leakage


def _box_ious(
    truth: list[Mapping[str, object]], result: Mapping[str, object] | None
) -> list[float]:
    left = {item["id"]: item["xyxy"] for item in truth}
    right = ({item["id"]: item["xyxy"] for item in result["boxes"]}
             if result else {})
    keys = set(left) | set(right)
    return [_iou(left.get(key), right.get(key)) for key in keys]


def _iou(left: list[float] | None, right: list[float] | None) -> float:
    if left is None or right is None:
        return 0.0
    width = max(0.0, min(left[2], right[2]) - max(left[0], right[0]))
    height = max(0.0, min(left[3], right[3]) - max(left[1], right[1]))
    intersection = width * height
    left_area = (left[2] - left[0]) * (left[3] - left[1])
    right_area = (right[2] - right[0]) * (right[3] - right[1])
    return intersection / (left_area + right_area - intersection)


def _kendall(truth: list[str], result: list[str]) -> float:
    if set(truth) != set(result):
        return 0.0
    rank = {item: index for index, item in enumerate(result)}
    pairs = len(truth) * (len(truth) - 1) // 2
    concordant = sum(
        rank[truth[left]] < rank[truth[right]]
        for left in range(len(truth)) for right in range(left + 1, len(truth))
    )
    return (2 * concordant - pairs) / pairs


def _edit_distance(left: str, right: str) -> int:
    require(len(left) * len(right) <= 4_000_000, "CER input is too large")
    previous = list(range(len(right) + 1))
    for row, left_char in enumerate(left, 1):
        current = [row]
        for column, right_char in enumerate(right, 1):
            current.append(min(
                current[-1] + 1, previous[column] + 1,
                previous[column - 1] + (left_char != right_char),
            ))
        previous = current
    return previous[-1]


def _macro_f1(truth: list[str], predicted: list[str]) -> float:
    scores = []
    for label in set(truth) | set(predicted):
        tp = sum(a == label and b == label for a, b in zip(truth, predicted))
        fp = sum(a != label and b == label for a, b in zip(truth, predicted))
        fn = sum(a == label and b != label for a, b in zip(truth, predicted))
        scores.append(2 * tp / (2 * tp + fp + fn))
    return statistics.mean(scores)


def _percentile(values: list[float], fraction: float) -> float:
    ordered = sorted(values)
    position = (len(ordered) - 1) * fraction
    lower = math.floor(position)
    upper = math.ceil(position)
    return ordered[lower] + (ordered[upper] - ordered[lower]) * (
        position - lower
    )
