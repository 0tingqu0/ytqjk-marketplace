"""Public API for digest-bound document acceptance."""

from __future__ import annotations

import hashlib
from pathlib import Path

from .acceptance_evidence import EvidenceBundle, load_evidence
from .acceptance_metrics import (
    AcceptanceValidationError,
    MetricAlgorithms,
    NotConfiguredError,
    compute_metrics,
)
from .acceptance_report import (
    METRIC_DEFINITIONS,
    AcceptanceFailure,
    AcceptanceReport,
    blocked_report,
    completed_report,
)


def assess_document_acceptance(
    manifest_path: str | Path,
    run_path: str | Path,
    *,
    algorithms: MetricAlgorithms | None = None,
) -> AcceptanceReport:
    """Compute from bound per-sample evidence; malformed evidence blocks."""
    ids: tuple[str, ...] = ()
    try:
        configured = algorithms or MetricAlgorithms()
        if not isinstance(configured, MetricAlgorithms):
            raise AcceptanceValidationError("algorithms config is invalid")
        if configured.teds_id is not None and not isinstance(
            configured.teds_id, str
        ):
            raise AcceptanceValidationError("algorithm id is invalid")
        ids = (configured.teds_id,) if configured.teds_id else ()
        bundle = load_evidence(manifest_path, run_path)
        metrics = compute_metrics(bundle.samples, configured)
        return completed_report(
            metrics, bundle.manifest_sha256, bundle.run_sha256,
            bundle.covered_facets, ids,
        )
    except NotConfiguredError as error:
        return blocked_report(str(error), ids)
    except AcceptanceValidationError as error:
        return blocked_report(f"FAIL_CLOSED: {error}", ids)
    except Exception as error:
        error_type = f"{type(error).__module__}.{type(error).__qualname__}"
        reference = hashlib.sha256(error_type.encode("utf-8")).hexdigest()
        return blocked_report(
            f"FAIL_CLOSED: unexpected_type_ref={reference[:16]}", ids
        )


def assert_document_acceptance(
    manifest_path: str | Path,
    run_path: str | Path,
    *,
    algorithms: MetricAlgorithms | None = None,
) -> AcceptanceReport:
    """Return PASS; raise for both measured FAIL and evidence BLOCK."""
    report = assess_document_acceptance(
        manifest_path, run_path, algorithms=algorithms
    )
    if not report.passed:
        raise AcceptanceFailure("; ".join(report.blocked or report.failures))
    return report


__all__ = [
    "AcceptanceFailure",
    "AcceptanceReport",
    "AcceptanceValidationError",
    "EvidenceBundle",
    "METRIC_DEFINITIONS",
    "MetricAlgorithms",
    "assess_document_acceptance",
    "assert_document_acceptance",
    "load_evidence",
]
