"""Public contracts for atomic bootstrap candidate imports."""

from __future__ import annotations

from dataclasses import dataclass

from .intake_contracts import ParsedDocument


@dataclass(frozen=True)
class CandidateImport:
    """One scanned candidate and its bootstrap governance declaration."""

    title: str
    parsed: ParsedDocument
    source_kind: str = "codex-bootstrap"
    governance_state: str = "CANDIDATE"


@dataclass(frozen=True)
class ImportReceipt:
    """Sanitized durable result for one import marker."""

    marker: str
    project_id: str
    status: str
    input_count: int
    created_documents: int
    deduplicated_documents: int
    provenance_added: int
    chunks_created: int
    schema_version: int
    receipt_sha256: str
