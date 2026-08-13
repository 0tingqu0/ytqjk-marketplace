"""Candidate isolation and side-effect-free promotion planning."""

from __future__ import annotations

import hashlib
import uuid

from .intake_contracts import (
    CONTROLLED_SCANNER_ID,
    HumanDecision,
    DecisionOrigin,
    IntakeCandidate,
    ParsedDocument,
    PromotionPlan,
    ScannerPort,
    ScanState,
)
from .intake_security import LocalScanner


_TARGET_STATES = frozenset({"APPROVED", "VERIFIED"})


class CandidateRegistry:
    """In-memory intake boundary; shared persistence integration is deferred."""

    def __init__(self, scanner: ScannerPort | None = None) -> None:
        self._scanner = scanner or LocalScanner()
        self._candidates: dict[str, IntakeCandidate] = {}
        self._dedupe: dict[tuple[str, str], str] = {}

    def add(
        self,
        project_id: str,
        parsed: ParsedDocument,
        *,
        purpose: str | None = None,
        ai_assessment: str | None = None,
    ) -> IntakeCandidate:
        """Validate proofs and store project-isolated candidate."""
        project = _project_uuid(project_id)
        replacement_count = _validate_proof(parsed, self._scanner)
        key = (project, parsed.content_sha256)
        duplicate = self._dedupe.get(key)
        if duplicate is not None:
            return self._candidates[duplicate]
        candidate_id = hashlib.sha256(
            f"candidate:{project}:{parsed.content_sha256}".encode("utf-8")
        ).hexdigest()
        metadata = {
            "source_path": parsed.source.relative_path,
            "source_sha256": parsed.source.sha256,
            "source_size_bytes": parsed.source.size_bytes,
            "purpose": _optional(purpose or parsed.source.purpose),
            "ai_assessment": _optional(ai_assessment),
            "encoding": "utf-8",
            "decode_errors": "replace",
            "replacement_count": replacement_count,
        }
        candidate = IntakeCandidate(
            id=candidate_id,
            project_id=project,
            state="CANDIDATE",
            content_sha256=parsed.content_sha256,
            source_sha256=parsed.source.sha256,
            document_id=parsed.document_id,
            version=1,
            deleted=False,
            chunks=parsed.chunks,
            metadata=metadata,
        )
        self._candidates[candidate_id] = candidate
        self._dedupe[key] = candidate_id
        return candidate

    def get(self, project_id: str, candidate_id: str) -> IntakeCandidate:
        """Return candidate only when project matches."""
        project = _project_uuid(project_id)
        candidate = self._candidates.get(candidate_id)
        if candidate is None:
            raise KeyError(candidate_id)
        if candidate.project_id != project:
            raise ValueError("project isolation violation")
        return candidate

    def plan_promotion(
        self,
        project_id: str,
        candidate_id: str,
        target_state: str,
        decision: HumanDecision,
        *,
        expected_version: int,
    ) -> PromotionPlan:
        """Build promotion DTO from explicit human decision."""
        candidate = self.get(project_id, candidate_id)
        if candidate.deleted or candidate.state != "CANDIDATE":
            raise ValueError("candidate is deleted or no longer CANDIDATE")
        if expected_version != candidate.version:
            raise ValueError("stale candidate version")
        target = target_state.strip().upper()
        if target not in _TARGET_STATES:
            raise ValueError("promotion target state is invalid")
        actor = _required(decision.actor, "human actor")
        if decision.origin is not DecisionOrigin.HUMAN:
            raise ValueError("human decision origin is required")
        reason = _required(decision.reason, "human reason")
        attestation = _required(decision.attestation or "", "human attestation")
        return PromotionPlan(
            command="PROMOTE_CANDIDATE",
            project_id=candidate.project_id,
            candidate_id=candidate.id,
            document_id=candidate.document_id,
            expected_state="CANDIDATE",
            expected_version=candidate.version,
            content_cas=candidate.content_sha256,
            target_state=target,
            actor=actor,
            reason=reason,
            attestation=attestation,
        )


def _required(value: str, name: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{name} is required")
    return value.strip()


def _optional(value: str | None) -> str | None:
    return value.strip() if isinstance(value, str) and value.strip() else None


def _project_uuid(value: str) -> str:
    project = _required(value, "project_id")
    try:
        return str(uuid.UUID(project))
    except ValueError as error:
        raise ValueError("project_id must be UUID") from error


def _validate_proof(parsed: ParsedDocument, scanner: ScannerPort) -> int:
    source = parsed.source
    from .intake_parsers import default_registry
    from .intake_security import inspect_input

    try:
        live_source = inspect_input(
            source.root, source.path, purpose=source.purpose, scanner=scanner
        )[0]
        live_parsed = default_registry(scanner=scanner).parse(live_source)
    except (OSError, RuntimeError, ValueError) as error:
        raise ValueError("source metadata proof is invalid") from error
    source_digest = hashlib.sha256(source.content).hexdigest()
    if (
        source != live_source
        or source.sha256 != source_digest
        or source.size_bytes != len(source.content)
        or source.scan.state is not ScanState.CLEAN
        or source.scan.sha256 != source_digest
        or source.scan.size_bytes != len(source.content)
        or source.scan.scanner != CONTROLLED_SCANNER_ID
    ):
        raise ValueError("source scanner proof is invalid")
    decoded = source.content.decode("utf-8", errors="replace")
    replacement_count = decoded.count("\ufffd")
    if (
        parsed.encoding != "utf-8"
        or parsed.decode_errors != "replace"
        or parsed.replacement_count != replacement_count
    ):
        raise ValueError("encoding proof is invalid")
    if parsed.text != live_parsed.text:
        raise ValueError("parser process proof is invalid")
    encoded = parsed.text.encode("utf-8")
    digest = hashlib.sha256(encoded).hexdigest()
    document_id = hashlib.sha256(
        f"document:{source_digest}:{digest}".encode("utf-8")
    ).hexdigest()
    if (
        parsed.content_sha256 != digest
        or parsed.document_id != document_id
        or parsed.output_scan.state is not ScanState.CLEAN
        or parsed.output_scan.sha256 != digest
        or parsed.output_scan.size_bytes != len(encoded)
        or parsed.output_scan.scanner != CONTROLLED_SCANNER_ID
        or parsed.output_scan.scanner != source.scan.scanner
        or parsed.output_scan != live_parsed.output_scan
    ):
        raise ValueError("parsed scanner proof is invalid")
    rebuilt = "".join(chunk.text for chunk in parsed.chunks)
    if not parsed.chunks or rebuilt != parsed.text:
        raise ValueError("chunk proof is invalid")
    for ordinal, chunk in enumerate(parsed.chunks, 1):
        chunk_digest = hashlib.sha256(chunk.text.encode("utf-8")).hexdigest()
        chunk_id = hashlib.sha256(
            f"{document_id}:{ordinal}:{chunk_digest}".encode("utf-8")
        ).hexdigest()
        if (
            chunk.parent_id != document_id
            or chunk.ordinal != ordinal
            or chunk.sha256 != chunk_digest
            or chunk.id != chunk_id
        ):
            raise ValueError("chunk proof is invalid")
    return replacement_count
