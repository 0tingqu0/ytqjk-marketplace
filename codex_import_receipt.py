"""Sanitized receipt helpers for the Codex bootstrap importer."""
from __future__ import annotations

from typing import Any


DATABASE_SCOPE = "global-candidates"


def new_receipt(
    status: str, marker_sha256: str | None = None
) -> dict[str, object]:
    """Create a receipt containing no source paths or content."""
    return {
        "status": status,
        "database_scope": DATABASE_SCOPE,
        "scanner": "NOT_EXECUTED",
        "marker_status": "NOT_CHECKED",
        "rollback": "NOT_APPLICABLE",
        "discovered_count": 0,
        "imported_count": 0,
        "deduplicated_count": 0,
        "provenance_count": 0,
        "chunk_count": 0,
        "excluded_count": 0,
        "not_configured_count": 0,
        "blocked_count": 0,
        "parse_failed_count": 0,
        "previous_imported_count": 0,
        "previous_deduplicated_count": 0,
        "previous_provenance_count": 0,
        "previous_chunk_count": 0,
        "manifest_sha256": None,
        "marker_sha256": marker_sha256,
        "service_receipt_sha256": None,
        "failure_stage": None,
        "failure_code": None,
    }


def apply_service_result(
    receipt: dict[str, object], result: Any, *, replay: bool
) -> None:
    """Map current atomic service counters into the install receipt."""
    receipt.update({
        "status": "SUCCEEDED",
        "marker_status": "REPLAYED" if replay else "WRITTEN",
        "imported_count": result.created_documents,
        "deduplicated_count": result.deduplicated_documents,
        "provenance_count": result.provenance_added,
        "chunk_count": result.chunks_created,
        "service_receipt_sha256": result.receipt_sha256,
    })


def apply_previous_result(
    receipt: dict[str, object], result: Any
) -> None:
    """Expose durable prior counters without presenting them as current work."""
    receipt.update({
        "status": "SKIPPED_MARKER",
        "marker_status": "HIT",
        "service_receipt_sha256": result.receipt_sha256,
        "previous_imported_count": result.created_documents,
        "previous_deduplicated_count": result.deduplicated_documents,
        "previous_provenance_count": result.provenance_added,
        "previous_chunk_count": result.chunks_created,
    })


def fail(
    receipt: dict[str, object], stage: str, code: str
) -> dict[str, object]:
    """Record a stable non-sensitive failure classification."""
    receipt.update({
        "status": "FAILED",
        "failure_stage": stage,
        "failure_code": code,
    })
    return receipt
