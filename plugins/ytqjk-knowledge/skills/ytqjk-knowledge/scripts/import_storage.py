"""Atomic SQLite implementation for bootstrap candidate imports."""

from __future__ import annotations

import hashlib
import sqlite3
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Sequence

from .database import connection, transaction
from .import_contracts import CandidateImport, ImportReceipt
from .import_receipts import build_receipt, read_receipt as _read_receipt
from .import_receipts import skipped_receipt, write_receipt
from .import_validation import name, source_ref, validate_candidates


def import_candidates(
    database: Path,
    project_scope: str,
    project_alias: str,
    marker: str,
    candidates: Sequence[CandidateImport],
    *,
    force: bool,
) -> ImportReceipt:
    """Validate then atomically ensure project, candidates, and marker."""
    scope = name(project_scope, "project scope")
    alias = name(project_alias, "project alias")
    safe_marker = name(marker, "import marker")
    items = validate_candidates(candidates)
    if not force:
        with connection(database) as current:
            existing = _checked_receipt(
                current, safe_marker, scope, alias
            )
            if existing is not None:
                return skipped_receipt(existing)
    with transaction(database) as current:
        existing = _checked_receipt(current, safe_marker, scope, alias)
        if existing is not None and not force:
            return skipped_receipt(existing)
        project_id = _ensure_project(current, scope, alias)
        counters = [0, 0, 0, 0]
        for item in items:
            _write_candidate(current, project_id, item, counters)
        receipt = build_receipt(
            safe_marker, project_id, len(items), counters
        )
        write_receipt(current, receipt, _now())
        return receipt


def read_receipt(database: Path, marker: str) -> ImportReceipt | None:
    """Read one sanitized, checksummed import receipt."""
    safe_marker = name(marker, "import marker")
    with connection(database) as current:
        return _read_receipt(current, safe_marker)


def _ensure_project(
    current: sqlite3.Connection, scope: str, alias: str
) -> str:
    row = current.execute(
        "SELECT id, scope FROM projects WHERE alias = ?", (alias,)
    ).fetchone()
    if row is not None:
        if row["scope"] != scope:
            raise ValueError("project alias belongs to another scope")
        return str(row["id"])
    project_id = str(uuid.uuid4())
    now = _now()
    current.execute(
        "INSERT INTO projects(id, name, scope, alias, created_at) "
        "VALUES (?, ?, ?, ?, ?)",
        (project_id, alias, scope, alias, now),
    )
    _audit(current, "bootstrap_project_created", project_id, now)
    return project_id


def _checked_receipt(
    current: sqlite3.Connection,
    marker: str,
    scope: str,
    alias: str,
) -> ImportReceipt | None:
    receipt = _read_receipt(current, marker)
    if receipt is None:
        return None
    row = current.execute(
        "SELECT scope, alias FROM projects WHERE id = ?",
        (receipt.project_id,),
    ).fetchone()
    if row is None or row["scope"] != scope or row["alias"] != alias:
        raise ValueError("import marker belongs to another project")
    return receipt


def _write_candidate(
    current: sqlite3.Connection,
    project_id: str,
    item: CandidateImport,
    counters: list[int],
) -> None:
    digest = item.parsed.content_sha256
    row = _current_document(current, project_id, digest)
    if row is None:
        current.execute(
            "DELETE FROM import_documents WHERE project_id = ? "
            "AND content_sha256 = ?",
            (project_id, digest),
        )
        document_id, version_id = _create_document(
            current, project_id, item
        )
        counters[0] += 1
        counters[3] += len(item.parsed.chunks)
        attach_source = True
    else:
        document_id = str(row["document_id"])
        version_id = int(row["version_id"])
        attach_source = row["version_state"] == "candidate"
        current.execute(
            "DELETE FROM import_documents WHERE project_id = ? AND "
            "(content_sha256 = ? OR document_id = ?)",
            (project_id, digest, document_id),
        )
        current.execute(
            "INSERT INTO import_documents(project_id, content_sha256, "
            "document_id, version_id) VALUES (?, ?, ?, ?)",
            (project_id, digest, document_id, version_id),
        )
        counters[1] += 1
    if _add_provenance(
        current, document_id, version_id, item, attach_source
    ):
        counters[2] += 1


def _current_document(
    current: sqlite3.Connection, project_id: str, digest: str
) -> sqlite3.Row | None:
    return current.execute(
        "SELECT d.id AS document_id, v.id AS version_id, "
        "v.state AS version_state FROM documents d "
        "JOIN versions v ON v.document_id = d.id "
        "WHERE d.project_id = ? AND d.deleted_at IS NULL "
        "AND v.original_sha256 = ? AND v.state != 'tombstone' "
        "AND v.ordinal = "
        "(SELECT MAX(latest.ordinal) FROM versions latest "
        "WHERE latest.document_id = d.id) ORDER BY d.id LIMIT 1",
        (project_id, digest),
    ).fetchone()


def _create_document(
    current: sqlite3.Connection,
    project_id: str,
    item: CandidateImport,
) -> tuple[str, int]:
    parsed = item.parsed
    document_id = str(uuid.uuid4())
    now = _now()
    current.execute(
        "INSERT OR IGNORE INTO originals(sha256, content, created_at) "
        "VALUES (?, ?, ?)",
        (parsed.content_sha256, parsed.text.encode("utf-8"), now),
    )
    current.execute(
        "INSERT INTO documents(id, project_id, title) VALUES (?, ?, ?)",
        (document_id, project_id, item.title.strip()),
    )
    version_id = int(current.execute(
        "INSERT INTO versions(document_id, ordinal, state, "
        "original_sha256, created_at) VALUES (?, 1, 'candidate', ?, ?)",
        (document_id, parsed.content_sha256, now),
    ).lastrowid)
    for chunk in parsed.chunks:
        chunk_id = hashlib.sha256(
            f"{version_id}:{chunk.ordinal}:{chunk.sha256}".encode("utf-8")
        ).hexdigest()
        current.execute(
            "INSERT INTO chunks(id, version_id, ordinal, content) "
            "VALUES (?, ?, ?, ?)",
            (chunk_id, version_id, chunk.ordinal, chunk.text),
        )
    current.execute(
        "INSERT INTO governance(version_id, action, actor, created_at) "
        "VALUES (?, 'candidate', ?, ?)",
        (version_id, "一听曲就困", now),
    )
    current.execute(
        "INSERT INTO import_documents(project_id, content_sha256, "
        "document_id, version_id) VALUES (?, ?, ?, ?)",
        (project_id, parsed.content_sha256, document_id, version_id),
    )
    _audit(current, "bootstrap_candidate_created", document_id, now)
    return document_id, version_id


def _add_provenance(
    current: sqlite3.Connection,
    document_id: str,
    version_id: int,
    item: CandidateImport,
    attach_source: bool,
) -> bool:
    source = item.parsed.source
    locator = source_ref(source.relative_path)
    key = (document_id, item.source_kind, locator)
    proof = (source.sha256, source.scan.scanner, "CLEAN", "CANDIDATE")
    existing = current.execute(
        "SELECT source_sha256, scanner, scan_state, governance_state "
        "FROM import_provenance WHERE document_id = ? AND source_kind = ? "
        "AND source_ref = ?",
        key,
    ).fetchone()
    if existing is not None:
        stored = tuple(existing[column] for column in existing.keys())
        if stored == proof:
            return False
        current.execute(
            "UPDATE import_provenance SET source_sha256 = ?, scanner = ?, "
            "scan_state = ?, governance_state = ? WHERE document_id = ? "
            "AND source_kind = ? AND source_ref = ?",
            proof + key,
        )
        return True
    current.execute(
        "INSERT INTO import_provenance(document_id, source_kind, source_ref, "
        "source_sha256, scanner, scan_state, governance_state) "
        "VALUES (?, ?, ?, ?, ?, ?, ?)",
        key + proof,
    )
    if attach_source:
        current.execute(
            "INSERT INTO sources(version_id, kind, locator) VALUES (?, ?, ?)",
            (version_id, item.source_kind, locator),
        )
    return True


def _audit(
    current: sqlite3.Connection, event: str, subject_id: str, now: str
) -> None:
    current.execute(
        "INSERT INTO audit(event, subject_id, created_at, detail) "
        "VALUES (?, ?, ?, '{}')",
        (event, subject_id, now),
    )


def _now() -> str:
    return datetime.now(timezone.utc).isoformat()
