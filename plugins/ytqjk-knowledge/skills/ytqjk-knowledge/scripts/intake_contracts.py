"""Typed contracts for isolated knowledge intake governance."""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from enum import Enum
from pathlib import Path
from typing import Any, Protocol


CONTROLLED_SCANNER_ID = "local-pattern-v1"


class CapabilityState(str, Enum):
    """Parser adapter configuration state."""

    CONFIGURED = "CONFIGURED"
    NOT_CONFIGURED = "NOT_CONFIGURED"


class ScanState(str, Enum):
    """Scanner decision state."""

    CLEAN = "CLEAN"
    BLOCKED = "BLOCKED"
    UNCERTAIN = "UNCERTAIN"
    UNAVAILABLE = "UNAVAILABLE"


class DecisionOrigin(str, Enum):
    """Controlled origin for promotion decisions."""

    HUMAN = "HUMAN"
    AI = "AI"
    SYSTEM = "SYSTEM"


class JobState(str, Enum):
    """Intake job lifecycle state."""

    QUEUED = "QUEUED"
    RUNNING = "RUNNING"
    SUCCEEDED = "SUCCEEDED"
    FAILED = "FAILED"
    CANCELLED = "CANCELLED"


@dataclass(frozen=True)
class ScanResult:
    """Digest-bound scanner output."""

    state: ScanState
    sha256: str
    size_bytes: int
    scanner: str


class ScannerPort(Protocol):
    """Port for deterministic content scanners."""

    def ready(self) -> bool:
        """Return whether scanner can accept work."""
        ...

    def scan(self, content: bytes, phase: str) -> ScanResult:
        """Scan bytes for named intake phase."""
        ...


@dataclass(frozen=True)
class IntakeJob:
    """Immutable intake job snapshot."""

    id: str
    project_id: str
    state: JobState
    payload: dict[str, Any]
    input_digest: str
    progress: int
    stage: str
    attempt: int
    owner: str | None
    error: str | None
    error_category: str | None = None
    lease_expires_at: datetime | None = None
    created_order: int = 0


@dataclass(frozen=True)
class ParserCapability:
    """Parser capability declaration."""

    extension: str
    media_kind: str
    state: CapabilityState
    adapter: str


@dataclass(frozen=True)
class InspectedSource:
    """Stable source bytes and verified metadata."""

    root: Path
    path: Path
    relative_path: str
    extension: str
    media_type: str | None
    content: bytes
    sha256: str
    size_bytes: int
    modified_ns: int
    changed_ns: int
    device: int
    inode: int
    scan: ScanResult
    purpose: str | None = None


@dataclass(frozen=True)
class ParsedChunk:
    """Deterministic child chunk of parsed document."""

    id: str
    parent_id: str
    ordinal: int
    text: str
    sha256: str


@dataclass(frozen=True)
class ParsedDocument:
    """Normalized parser output with scan proof."""

    document_id: str
    source: InspectedSource
    text: str
    content_sha256: str
    output_scan: ScanResult
    encoding: str
    decode_errors: str
    replacement_count: int
    chunks: tuple[ParsedChunk, ...]


@dataclass(frozen=True)
class HumanDecision:
    """Explicit promotion decision from controlled origin."""

    actor: str
    reason: str
    origin: DecisionOrigin
    attestation: str | None = None


@dataclass(frozen=True)
class IntakeCandidate:
    """Project-isolated candidate knowledge record."""

    id: str
    project_id: str
    state: str
    content_sha256: str
    source_sha256: str
    document_id: str
    version: int
    deleted: bool
    chunks: tuple[ParsedChunk, ...]
    metadata: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class PromotionPlan:
    """Validated side-effect-free promotion command DTO."""

    command: str
    project_id: str
    candidate_id: str
    document_id: str
    expected_state: str
    expected_version: int
    content_cas: str
    target_state: str
    actor: str
    reason: str
    attestation: str
