"""Sanitized, schema-validated bootstrap import receipt checksums."""

from __future__ import annotations

import hashlib
import hmac
import json
import sqlite3
from dataclasses import replace

from .import_contracts import ImportReceipt


RECEIPT_SCHEMA_VERSION = 4
SUPPORTED_RECEIPT_SCHEMA_VERSIONS = frozenset({3, RECEIPT_SCHEMA_VERSION})
FIELDS = frozenset(
    {
        "marker", "project_id", "status", "input_count",
        "created_documents", "deduplicated_documents",
        "provenance_added", "chunks_created", "schema_version",
    }
)
COUNTERS = (
    "input_count", "created_documents", "deduplicated_documents",
    "provenance_added", "chunks_created",
)


def build_receipt(
    marker: str,
    project_id: str,
    input_count: int,
    counters: list[int],
) -> ImportReceipt:
    """Build a receipt containing no source path, username, or content."""
    values = {
        "marker": marker,
        "project_id": project_id,
        "status": "IMPORTED",
        "input_count": input_count,
        "created_documents": counters[0],
        "deduplicated_documents": counters[1],
        "provenance_added": counters[2],
        "chunks_created": counters[3],
        "schema_version": RECEIPT_SCHEMA_VERSION,
    }
    _validate_values(values, marker, project_id)
    digest = hashlib.sha256(_json(values).encode("utf-8")).hexdigest()
    return ImportReceipt(**values, receipt_sha256=digest)


def write_receipt(
    current: sqlite3.Connection, receipt: ImportReceipt, completed_at: str
) -> None:
    """Insert or atomically replace the durable marker receipt."""
    payload = _receipt_payload(receipt)
    current.execute(
        "INSERT INTO import_receipts(marker, project_id, receipt, "
        "receipt_sha256, completed_at) VALUES (?, ?, ?, ?, ?) "
        "ON CONFLICT(marker) DO UPDATE SET project_id = excluded.project_id, "
        "receipt = excluded.receipt, receipt_sha256 = excluded.receipt_sha256, "
        "completed_at = excluded.completed_at",
        (
            receipt.marker,
            receipt.project_id,
            payload,
            receipt.receipt_sha256,
            completed_at,
        ),
    )


def read_receipt(
    current: sqlite3.Connection, marker: str
) -> ImportReceipt | None:
    """Read one checksummed receipt with row and payload binding."""
    row = current.execute(
        "SELECT project_id, receipt, receipt_sha256 FROM import_receipts "
        "WHERE marker = ?",
        (marker,),
    ).fetchone()
    if row is None:
        return None
    try:
        values = json.loads(
            str(row["receipt"]), object_pairs_hook=_unique_object
        )
        _validate_values(values, marker, str(row["project_id"]))
        expected = hashlib.sha256(_json(values).encode("utf-8")).hexdigest()
        stored = row["receipt_sha256"]
        if not isinstance(stored, str) or not hmac.compare_digest(
            expected, stored
        ):
            raise ValueError("receipt checksum mismatch")
    except (TypeError, ValueError, json.JSONDecodeError) as error:
        raise RuntimeError("import receipt integrity check failed") from error
    try:
        return ImportReceipt(**values, receipt_sha256=expected)
    except TypeError as error:
        raise RuntimeError("import receipt integrity check failed") from error


def skipped_receipt(receipt: ImportReceipt) -> ImportReceipt:
    """Build a checksummed call result without changing durable state."""
    skipped = replace(receipt, status="SKIPPED", receipt_sha256="")
    digest = hashlib.sha256(
        _receipt_payload(skipped).encode("utf-8")
    ).hexdigest()
    return replace(skipped, receipt_sha256=digest)


def _receipt_payload(receipt: ImportReceipt) -> str:
    values = dict(receipt.__dict__)
    values.pop("receipt_sha256")
    return _json(values)


def _json(value: object) -> str:
    return json.dumps(
        value, ensure_ascii=True, sort_keys=True, separators=(",", ":")
    )


def _validate_values(
    values: object, expected_marker: str, expected_project: str
) -> None:
    if not isinstance(values, dict) or set(values) != FIELDS:
        raise ValueError("receipt fields are invalid")
    if values["marker"] != expected_marker:
        raise ValueError("receipt marker binding failed")
    if values["project_id"] != expected_project:
        raise ValueError("receipt project binding failed")
    if values["status"] != "IMPORTED":
        raise ValueError("receipt status is invalid")
    if (
        type(values["schema_version"]) is not int
        or values["schema_version"] not in SUPPORTED_RECEIPT_SCHEMA_VERSIONS
    ):
        raise ValueError("receipt schema version is invalid")
    for field in COUNTERS:
        value = values[field]
        if type(value) is not int or value < 0:
            raise ValueError("receipt counter is invalid")
    if values["input_count"] != (
        values["created_documents"] + values["deduplicated_documents"]
    ):
        raise ValueError("receipt counters are inconsistent")
    if values["provenance_added"] > values["input_count"]:
        raise ValueError("receipt provenance counter is inconsistent")


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate receipt field")
        result[key] = value
    return result
