"""Acceptance status construction over computed metrics."""

from __future__ import annotations

import math
from dataclasses import dataclass
from types import MappingProxyType
from typing import Mapping

FACETS = frozenset({
    "native", "scan", "mixed", "table", "image", "corrupt",
    "clear", "noisy", "tilted",
})
ROUTE_FACETS = frozenset({"native", "scan", "mixed"})
ROUTES = frozenset({"NATIVE_TEXT", "OCR", "MIXED"})
LOW_CONFIDENCE_CUTOFF = 0.88
MAX_ITEMS, MAX_TEXT, MAX_NUMBER = 10_000, 100_000, 1_000_000_000.0


class AcceptanceValidationError(ValueError):
    pass


METRIC_DEFINITIONS = {
    "native_cer": "ratio; total edit distance / truth native characters",
    "clear_scan_cer": "ratio; same CER for scan+clear truth pages",
    "noisy_scan_cer": "ratio; same CER for scan+noisy truth pages",
    "routing_accuracy": "ratio; exact route matches / truth pages",
    "missing_page_count": "count; absent truth page numbers",
    "duplicate_page_count": "count; extra or unknown result page records",
    "reading_order_kendall": "mean Kendall tau-a; set mismatch scores zero",
    "table_teds": "mean score from configured TEDS adapter",
    "table_cell_accuracy": "ratio; exact id+text cells / union cell ids",
    "image_bbox_iou_median": "ratio; median xyxy IoU over union block ids",
    "image_bbox_iou_p05": "ratio; linear-interpolated 5th percentile IoU",
    "retry_success_rate": "ratio; succeeded / truth retry target ids",
    "conflict_review_rate": "ratio; reviewed truth conflicts / expected",
    "review_trigger_recall": (
        "ratio; reviewed / union of truth review targets and result "
        "confidence below 0.88"
    ),
    "corrupt_rejection_accuracy": (
        "ratio; corrupt samples with exact REJECTED status+error category"
    ),
    "image_macro_f1": "ratio; unweighted F1 over union image labels",
    "image_top_3_recall": "ratio; truth image class present in top three",
    "retrieval_recall_at_5": "ratio; mean relevant ids found in first five",
    "candidate_leakage_count": "count; candidate hits in retrieval top five",
    "warm_mean_seconds": "seconds; arithmetic mean of warm timings",
    "warm_median_seconds": "seconds; median of warm timings",
    "warm_p95_seconds": "seconds; linear-interpolated warm percentile 95",
    "native_p95_seconds": "seconds; warm percentile 95 for native samples",
}
LOWER_THRESHOLDS = {
    "routing_accuracy": 1.0, "reading_order_kendall": 0.98,
    "table_teds": 0.95, "table_cell_accuracy": 0.98,
    "image_bbox_iou_median": 0.90, "image_bbox_iou_p05": 0.75,
    "retry_success_rate": 1.0, "conflict_review_rate": 1.0,
    "review_trigger_recall": 1.0, "corrupt_rejection_accuracy": 1.0,
    "image_macro_f1": 0.90,
    "image_top_3_recall": 0.98, "retrieval_recall_at_5": 1.0,
}
UPPER_THRESHOLDS = {
    "native_cer": 0.0, "clear_scan_cer": 0.01,
    "noisy_scan_cer": 0.03, "missing_page_count": 0.0,
    "duplicate_page_count": 0.0, "candidate_leakage_count": 0.0,
    "warm_mean_seconds": 60.0, "warm_median_seconds": 45.0,
    "warm_p95_seconds": 90.0, "native_p95_seconds": 20.0,
}


class AcceptanceFailure(AssertionError):
    pass


@dataclass(frozen=True, slots=True)
class AcceptanceReport:
    """PASS/FAIL/BLOCK plus only metrics computed from bound evidence."""

    status: str
    metrics: Mapping[str, float] | None
    failures: tuple[str, ...]
    blocked: tuple[str, ...]
    manifest_sha256: str | None
    run_sha256: str | None
    covered_facets: frozenset[str]
    algorithm_ids: tuple[str, ...]

    def __post_init__(self) -> None:
        if self.metrics is not None:
            immutable = MappingProxyType(dict(self.metrics))
            object.__setattr__(self, "metrics", immutable)

    @property
    def passed(self) -> bool:
        return self.status == "PASS"


def completed_report(
    metrics: Mapping[str, float], manifest_sha256: str, run_sha256: str,
    facets: frozenset[str], algorithm_ids: tuple[str, ...],
) -> AcceptanceReport:
    failures = [
        f"{name}={metrics[name]:g} > {limit:g}"
        for name, limit in UPPER_THRESHOLDS.items() if metrics[name] > limit
    ]
    failures.extend(
        f"{name}={metrics[name]:g} < {limit:g}"
        for name, limit in LOWER_THRESHOLDS.items() if metrics[name] < limit
    )
    status = "FAIL" if failures else "PASS"
    return AcceptanceReport(
        status, metrics, tuple(failures), (), manifest_sha256, run_sha256,
        facets, algorithm_ids,
    )


def blocked_report(
    reason: str, algorithm_ids: tuple[str, ...]
) -> AcceptanceReport:
    return AcceptanceReport(
        "BLOCK", None, (), (reason,), None, None, frozenset(), algorithm_ids
    )


def bounded_fields(value: object, expected: set[str], label: str) -> None:
    require(isinstance(value, Mapping) and set(value) == expected,
            f"{label} fields are invalid")


def bounded_array(
    value: object, label: str, *, required: bool = False
) -> list:
    require(isinstance(value, list) and len(value) <= MAX_ITEMS,
            f"{label} must be bounded array")
    require(not required or bool(value), f"{label} must be non-empty")
    return value


def bounded_ids(value: object, label: str) -> list[str]:
    items = bounded_array(value, label)
    for item in items:
        bounded_text(item, label)
    unique(items, label)
    return items


def unique(values: object, label: str) -> None:
    items = list(values)
    safe = all(isinstance(item, str) for item in items)
    require(safe and len(items) == len(set(items)), f"{label} must be unique")


def bounded_text(value: object, label: str, *, empty: bool = False) -> str:
    require(isinstance(value, str) and len(value) <= MAX_TEXT,
            f"{label} must be bounded text")
    require(empty or bool(value), f"{label} must be non-empty")
    return value


def bounded_integer(value: object, label: str, low: int) -> int:
    require(not isinstance(value, bool) and isinstance(value, int),
            f"{label} must be integer")
    require(low <= value <= MAX_NUMBER, f"{label} is out of range")
    return value


def bounded_number(
    value: object, label: str, *, low: float = -MAX_NUMBER,
    high: float = MAX_NUMBER,
) -> float:
    require(not isinstance(value, bool) and isinstance(value, (int, float)),
            f"{label} must be numeric")
    number = float(value)
    require(math.isfinite(number) and low <= number <= high,
            f"{label} is out of range")
    return number


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AcceptanceValidationError(message)


def validate_table(value: object) -> None:
    if value is None:
        return
    bounded_fields(value, {"structure", "cells"}, "table")
    bounded_text(value["structure"], "structure", empty=True)
    cells = bounded_array(value["cells"], "cells")
    for cell in cells:
        bounded_fields(cell, {"id", "text"}, "cell")
        bounded_text(cell["id"], "cell.id")
        bounded_text(cell["text"], "cell.text", empty=True)
    unique((cell["id"] for cell in cells), "cell ids")


def validate_boxes(value: object) -> None:
    boxes = bounded_array(value, "boxes")
    for box in boxes:
        bounded_fields(box, {"id", "xyxy"}, "box")
        bounded_text(box["id"], "box.id")
        coords = bounded_array(box["xyxy"], "xyxy")
        require(len(coords) == 4, "xyxy needs four values")
        xyxy = [bounded_number(item, "coordinate") for item in coords]
        require(xyxy[2] > xyxy[0] and xyxy[3] > xyxy[1],
                "box has non-positive area")
    unique((box["id"] for box in boxes), "box ids")


def validate_queries(value: object, *, truth: bool) -> None:
    queries = bounded_array(value, "queries")
    for query in queries:
        fields = {"id", "relevant_document_ids"} if truth else {"id", "top5"}
        bounded_fields(query, fields, "query")
        bounded_text(query["id"], "query.id")
        if truth:
            relevant = bounded_ids(
                query["relevant_document_ids"], "relevant ids"
            )
            require(bool(relevant), "relevant ids are empty")
        else:
            _validate_hits(query["top5"])
    unique((query["id"] for query in queries), "query ids")


def _validate_hits(value: object) -> None:
    hits = bounded_array(value, "top5")
    require(len(hits) <= 5, "top5 exceeds five")
    for hit in hits:
        bounded_fields(hit, {"document_id", "governance"}, "hit")
        bounded_text(hit["document_id"], "document id")
        allowed = ("approved", "verified", "candidate")
        require(hit["governance"] in allowed, "governance is invalid")
    unique((hit["document_id"] for hit in hits), "hit ids")


def validate_events(value: object, field: str) -> None:
    events = bounded_array(value, "events")
    for event in events:
        bounded_fields(event, {"id", field}, "event")
        bounded_text(event["id"], "event.id")
        require(isinstance(event[field], bool), f"{field} must be boolean")
    unique((event["id"] for event in events), "event ids")


def validate_coverage(
    samples: list[frozenset[str]],
) -> frozenset[str]:
    covered = frozenset().union(*samples)
    missing = FACETS - covered
    require(not missing, f"golden facet coverage missing: {sorted(missing)}")
    require(len(samples) >= 4, "golden set requires diverse samples")
    for route in ROUTE_FACETS:
        require(any(route in facets for facets in samples),
                f"golden route facet missing: {route}")
    return covered


def validate_sample_contract(facets: frozenset[str], pages: int) -> None:
    routes = facets & ROUTE_FACETS
    if "corrupt" in facets:
        require(facets == {"corrupt"} and pages == 0,
                "corrupt sample cannot cover other facets")
        return
    require(len(routes) == 1 and pages > 0,
            "non-corrupt sample needs exactly one route facet")
    require(not {"clear", "noisy"} <= facets,
            "clear and noisy require distinct samples")
    if facets & {"clear", "noisy", "tilted"}:
        require(bool(routes & {"scan", "mixed"}),
                "scan quality facet needs scan or mixed route")


def validate_page_contract(
    sample_facets: frozenset[str], facets: frozenset[str], route: str
) -> None:
    routes = facets & ROUTE_FACETS
    expected = {"NATIVE_TEXT": "native", "OCR": "scan", "MIXED": "mixed"}
    require(len(routes) == 1 and expected[route] in routes,
            "page route facet is inconsistent")
    non_route = facets - ROUTE_FACETS
    require(non_route <= sample_facets - ROUTE_FACETS,
            "page facets exceed sample facets")
    sample_route = next(iter(sample_facets & ROUTE_FACETS))
    if sample_route != "mixed":
        require(expected[route] == sample_route,
                "page route conflicts with sample route")
