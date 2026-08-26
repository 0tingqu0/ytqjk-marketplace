"""Validated domain records for durable document-intake jobs."""

from __future__ import annotations

import hashlib
import json
import math
import re
from dataclasses import dataclass
from enum import Enum
from pathlib import PurePosixPath
from typing import Mapping


SCHEMA_VERSION = 1
MAX_PAGE_COUNT = 10_000
DOCUMENT_JOB_SCHEMA = (
    """CREATE TABLE document_intake_jobs(
        id TEXT PRIMARY KEY,
        idempotency_key TEXT NOT NULL UNIQUE CHECK(length(idempotency_key)=64),
        state TEXT NOT NULL CHECK(state IN
            ('QUEUED','RUNNING','SUCCEEDED','FAILED','CANCELLED')),
        stage_index INTEGER NOT NULL CHECK(stage_index BETWEEN 0 AND 11),
        stage TEXT NOT NULL,
        progress INTEGER NOT NULL CHECK(progress BETWEEN 0 AND 100),
        page_count INTEGER CHECK(page_count BETWEEN 1 AND 10000),
        payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
        config_json TEXT NOT NULL CHECK(json_valid(config_json)),
        attempt INTEGER NOT NULL CHECK(attempt >= 0),
        revision INTEGER NOT NULL CHECK(revision >= 0), owner TEXT,
        heartbeat_at REAL, lease_expires_at REAL,
        error_category TEXT, error_ref TEXT,
        created_at REAL NOT NULL, updated_at REAL NOT NULL,
        CHECK((error_category IS NULL) = (error_ref IS NULL)),
        CHECK((state='RUNNING' AND owner IS NOT NULL
            AND heartbeat_at IS NOT NULL AND lease_expires_at IS NOT NULL)
            OR (state!='RUNNING' AND owner IS NULL
            AND heartbeat_at IS NULL AND lease_expires_at IS NULL)),
        CHECK(state!='SUCCEEDED' OR progress=100),
        CHECK(state='SUCCEEDED' OR progress<100)
    ) STRICT""",
    """CREATE TABLE document_intake_job_revisions(
        job_id TEXT NOT NULL REFERENCES document_intake_jobs(id),
        revision INTEGER NOT NULL, event TEXT NOT NULL,
        state TEXT NOT NULL, stage TEXT NOT NULL, progress INTEGER NOT NULL,
        created_at REAL NOT NULL,
        PRIMARY KEY(job_id, revision)
    ) STRICT""",
)
STAGES = (
    "validate",
    "security-scan",
    "page-detect",
    "native-extract",
    "render",
    "ocr-primary",
    "ocr-review",
    "layout-table",
    "normalize",
    "chunk",
    "candidate-write",
    "complete",
)
PAGE_STAGES = frozenset({
    "native-extract", "render", "ocr-primary", "ocr-review",
    "layout-table",
})
ERROR_CATEGORIES = frozenset({
    "INTERNAL", "LAYOUT_FAILED", "OCR_FAILED", "PAGE_DETECT_FAILED",
    "SECURITY_FAILED", "TRANSIENT", "VALIDATION_FAILED",
    "WORKER_EXPIRED", "WRITE_FAILED",
})
NON_RETRYABLE = frozenset({"SECURITY_FAILED", "VALIDATION_FAILED"})
_DIGEST = re.compile(r"[0-9a-f]{64}\Z")
_PAYLOAD_KEYS = frozenset({
    "staging_ref", "source_sha256", "manifest_sha256",
    "sidecar_sha256",
})
_REQUIRED_PAYLOAD = frozenset({"staging_ref", "source_sha256"})
_SENSITIVE = frozenset({
    "authorization", "cookie", "credential", "password", "secret",
    "token", "api_key", "private_key",
})


class JobValidationError(ValueError):
    pass


class JobTransitionError(ValueError):
    pass


class LeaseLostError(RuntimeError):
    pass


class JobState(str, Enum):
    QUEUED = "QUEUED"
    RUNNING = "RUNNING"
    SUCCEEDED = "SUCCEEDED"
    FAILED = "FAILED"
    CANCELLED = "CANCELLED"


@dataclass(frozen=True, slots=True)
class DocumentIntakeJob:
    id: str
    state: JobState
    stage: str
    progress: int
    page_count: int | None
    attempt: int
    revision: int
    owner: str | None
    heartbeat_at: float | None
    lease_expires_at: float | None
    payload: Mapping[str, str]
    config: Mapping[str, object]
    idempotency_key: str
    error_category: str | None
    error_ref: str | None
    created_at: float
    updated_at: float


def encode_payload(value: object) -> str:
    if not isinstance(value, Mapping):
        raise JobValidationError("payload must be an object")
    if not all(isinstance(key, str) for key in value):
        raise JobValidationError("payload keys must be strings")
    keys = frozenset(value)
    if not _REQUIRED_PAYLOAD <= keys or not keys <= _PAYLOAD_KEYS:
        raise JobValidationError("payload fields are invalid")
    normalized = dict(value)
    normalized["staging_ref"] = _staging_ref(value["staging_ref"])
    for key in keys - {"staging_ref"}:
        normalized[key] = _sha256(value[key], key)
    return _encoded(normalized, "payload", 4_096)


def encode_config(value: object) -> str:
    if not isinstance(value, Mapping):
        raise JobValidationError("config must be an object")
    normalized = _json_value(value, 0)
    return _encoded(normalized, "config", 65_536)


def decode_object(raw: object, label: str) -> dict[str, object]:
    if not isinstance(raw, str):
        raise JobValidationError(f"stored {label} must be JSON text")
    try:
        value = json.loads(
            raw,
            object_pairs_hook=_unique_object,
            parse_constant=_reject_constant,
        )
    except (ValueError, json.JSONDecodeError, RecursionError) as error:
        raise JobValidationError(f"stored {label} JSON is invalid") from error
    if not isinstance(value, dict):
        raise JobValidationError(f"stored {label} must be an object")
    encoded = encode_payload(value) if label == "payload" else encode_config(
        value
    )
    if encoded != raw:
        raise JobValidationError(f"stored {label} JSON is not canonical")
    return value


def decode_job_record(row: Mapping[str, object]) -> DocumentIntakeJob:
    payload = decode_object(row["payload_json"], "payload")
    config = decode_object(row["config_json"], "config")
    key = idempotency_key(str(row["payload_json"]), str(row["config_json"]))
    state = JobState(row["state"])
    stage_index = int(row["stage_index"])
    expected = 100 if state is JobState.SUCCEEDED else stage_progress(
        stage_index, row["page_count"]
    )
    valid = (
        row["idempotency_key"] == key
        and row["stage"] == STAGES[stage_index]
        and row["progress"] == expected
    )
    category, reference = row["error_category"], row["error_ref"]
    valid_error = (category is None and reference is None) or (
        category in ERROR_CATEGORIES
        and isinstance(reference, str)
        and bool(_DIGEST.fullmatch(reference))
    )
    if not valid or not valid_error:
        raise JobValidationError("stored job invariant mismatch")
    return DocumentIntakeJob(
        id=str(row["id"]), state=state, stage=str(row["stage"]),
        progress=int(row["progress"]), page_count=row["page_count"],
        attempt=int(row["attempt"]), revision=int(row["revision"]),
        owner=row["owner"], heartbeat_at=row["heartbeat_at"],
        lease_expires_at=row["lease_expires_at"], payload=payload,
        config=config, idempotency_key=key, error_category=category,
        error_ref=reference, created_at=float(row["created_at"]),
        updated_at=float(row["updated_at"]),
    )


def idempotency_key(payload_json: str, config_json: str) -> str:
    material = f"v{SCHEMA_VERSION}\n{payload_json}\n{config_json}"
    return hashlib.sha256(material.encode("utf-8")).hexdigest()


def stage_progress(stage_index: int, page_count: int | None) -> int:
    if not 0 <= stage_index < len(STAGES):
        raise JobValidationError("stage index is invalid")
    if page_count is None:
        if stage_index > STAGES.index("page-detect"):
            raise JobValidationError("page count is missing")
        return 0
    pages = validate_page_count(page_count)
    weights = [pages if stage in PAGE_STAGES else 1 for stage in STAGES[:-1]]
    completed_units = sum(weights[:stage_index])
    return min(99, 99 * completed_units // sum(weights))


def validate_page_count(value: object) -> int:
    valid = not isinstance(value, bool) and isinstance(value, int)
    if not valid or not 1 <= value <= MAX_PAGE_COUNT:
        raise JobValidationError("page count is invalid")
    return value


def validate_owner(value: object) -> str:
    if not isinstance(value, str) or value != value.strip():
        raise JobValidationError("owner is invalid")
    if not value or len(value) > 128 or any(ord(char) < 32 for char in value):
        raise JobValidationError("owner is invalid")
    return value


def validate_store_options(
    lease_seconds: object, max_attempts: object, clock: object
) -> tuple[float, int]:
    lease_ok = (
        not isinstance(lease_seconds, bool)
        and isinstance(lease_seconds, (int, float))
        and math.isfinite(float(lease_seconds))
        and 0 < lease_seconds <= 3_600
    )
    attempts_ok = (
        not isinstance(max_attempts, bool)
        and isinstance(max_attempts, int)
        and 1 <= max_attempts <= 100
    )
    invalid_clock = clock is not None and not callable(clock)
    if not lease_ok or not attempts_ok or invalid_clock:
        raise JobValidationError("store options are invalid")
    return float(lease_seconds), max_attempts


def normalize_error(category: object, detail: object) -> tuple[str, str]:
    normalized = category.upper() if isinstance(category, str) else ""
    if normalized not in ERROR_CATEGORIES:
        raise JobValidationError("error category is invalid")
    if not isinstance(detail, str) or not detail or len(detail) > 100_000:
        raise JobValidationError("error detail is invalid")
    reference = hashlib.sha256(detail.encode("utf-8")).hexdigest()
    return normalized, reference


def _staging_ref(value: object) -> str:
    if not isinstance(value, str) or not value or len(value) > 512:
        raise JobValidationError("staging_ref is invalid")
    path = PurePosixPath(value)
    invalid = (
        path.is_absolute()
        or path.as_posix() != value
        or ".." in path.parts
        or ":" in value
        or "\\" in value
        or any(ord(char) < 32 for char in value)
    )
    if invalid or not path.name:
        raise JobValidationError("staging_ref must be a relative POSIX path")
    return value


def _sha256(value: object, label: str) -> str:
    if not isinstance(value, str) or not _DIGEST.fullmatch(value):
        raise JobValidationError(f"{label} must be lowercase sha256")
    return value


def _json_value(value: object, depth: int) -> object:
    if depth > 8:
        raise JobValidationError("config nesting is too deep")
    if value is None or isinstance(value, (bool, str)):
        if isinstance(value, str) and len(value) > 4_096:
            raise JobValidationError("config string is too large")
        return value
    if not isinstance(value, bool) and isinstance(value, int):
        if abs(value) > 1_000_000_000:
            raise JobValidationError("config number is invalid")
        return value
    if isinstance(value, float):
        if not math.isfinite(value) or abs(value) > 1_000_000_000:
            raise JobValidationError("config number is invalid")
        return value
    if isinstance(value, list):
        return [_json_value(item, depth + 1) for item in value]
    if isinstance(value, Mapping):
        output = {}
        for key, item in value.items():
            if not isinstance(key, str) or not key or len(key) > 128:
                raise JobValidationError("config key is invalid")
            lowered = key.lower().replace("-", "_")
            if lowered in _SENSITIVE or any(
                lowered.endswith(f"_{name}") for name in _SENSITIVE
            ):
                raise JobValidationError("config contains a secret field")
            output[key] = _json_value(item, depth + 1)
        return output
    raise JobValidationError("config must contain strict JSON values")


def _encoded(value: object, label: str, limit: int) -> str:
    try:
        encoded = json.dumps(
            value,
            allow_nan=False, ensure_ascii=False,
            separators=(",", ":"), sort_keys=True,
        )
    except (TypeError, ValueError) as error:
        raise JobValidationError(f"{label} is not strict JSON") from error
    if len(encoded.encode("utf-8")) > limit:
        raise JobValidationError(f"{label} is too large")
    return encoded


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    output = {}
    for key, value in pairs:
        if key in output:
            raise JobValidationError(f"duplicate JSON key: {key}")
        output[key] = value
    return output


def _reject_constant(value: str) -> None:
    raise JobValidationError(f"invalid JSON number: {value}")
