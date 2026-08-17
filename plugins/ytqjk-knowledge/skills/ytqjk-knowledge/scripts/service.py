"""Public local SQLite boundary for all YTQJK Knowledge adapters."""

from __future__ import annotations

import hashlib
import json
import threading
import uuid
from pathlib import Path
from typing import Any, Sequence

from .database import connection, read_row, read_rows, read_value
from .domain import apply
from .import_contracts import CandidateImport, ImportReceipt
from .import_storage import import_candidates, read_receipt
from .migrations import LATEST_VERSION, migrate
from .queue import _WriteQueue


class KnowledgeService:
    """Only public owner of knowledge storage, writes, and snapshot reads."""

    def __init__(self, database: Path) -> None:
        self._database = database
        self._writer = threading.Lock()
        database.parent.mkdir(parents=True, exist_ok=True)
        with connection(database) as current:
            migrate(current)
        self._queue = _WriteQueue(database)

    def migrate(self, target: int = LATEST_VERSION) -> None:
        """Move DB between supported schema versions."""
        with self._writer, connection(self._database) as current:
            migrate(current, target)

    def schema_version(self) -> int:
        return read_value(self._database, "PRAGMA user_version")

    def create_project(self, scope: str, alias: str) -> str:
        """Create or return immutable project identity."""
        payload = {"id": str(uuid.uuid4()), "scope": scope, "alias": alias}
        key = self._key("project", {"scope": scope, "alias": alias})
        self._submit("create_project", payload, key)
        rows = read_rows(self._database, "SELECT id FROM projects WHERE scope = ? AND alias = ?", (scope, alias))
        return str(rows[0]["id"])

    def import_candidates(
        self,
        project_scope: str,
        project_alias: str,
        marker: str,
        candidates: Sequence[CandidateImport],
        *,
        force: bool = False,
    ) -> ImportReceipt:
        """Atomically ensure project, candidates, provenance, and marker."""
        with self._writer:
            return import_candidates(
                self._database,
                project_scope,
                project_alias,
                marker,
                candidates,
                force=force,
            )

    def import_receipt(self, marker: str) -> ImportReceipt | None:
        """Read one schema-validated bootstrap receipt checksum."""
        return read_receipt(self._database, marker)

    def create_candidate(self, project_id: str, title: str, content: str, source: str) -> str:
        """Create or return deduplicated candidate document."""
        document_id = str(uuid.uuid4())
        payload = {"document_id": document_id, "project_id": project_id, "title": title, "content": content, "source": source}
        key = self._key("candidate", {key: value for key, value in payload.items() if key != "document_id"})
        job_id = self._submit("create_candidate", payload, key)
        return str(self.job(job_id)["payload_document_id"])

    def edit_candidate(self, document_id: str, content: str, source: str) -> None:
        self._submit("edit_candidate", {"document_id": document_id, "content": content, "source": source})

    def soft_delete_candidate(self, document_id: str) -> None:
        self._submit("soft_delete_candidate", {"document_id": document_id})

    def append_state(self, document_id: str, state: str, content: str | None = None) -> None:
        self._submit("append_state", {"document_id": document_id, "state": state, "content": content})

    def record_feedback(
        self, document_id: str, invocation_id: str, correct: bool
    ) -> None:
        """Apply one idempotent, invocation-bound lifecycle decision."""
        if self.schema_version() < 4:
            raise RuntimeError("feedback lifecycle requires schema v4")
        payload = {
            "document_id": document_id,
            "invocation_id": invocation_id,
            "correct": correct,
        }
        self._submit("record_feedback", payload, self._key("feedback", payload))

    def feedback_status(self, document_id: str) -> dict[str, Any]:
        """Read the latest explicit feedback result for one document."""
        return read_row(
            self._database,
            "SELECT invocation_id, correct, score, state, created_at "
            "FROM feedback_events WHERE document_id = ? ORDER BY id DESC LIMIT 1",
            (document_id,),
        )

    def recycle_bin(self, project_id: str) -> list[dict[str, Any]]:
        """List active project documents whose latest version is tombstoned."""
        return read_rows(
            self._database,
            "SELECT d.id, d.title, v.id AS version_id, v.created_at "
            "FROM documents d JOIN versions v ON v.document_id = d.id "
            "WHERE d.project_id = ? AND d.deleted_at IS NULL AND "
            "v.ordinal = (SELECT MAX(latest.ordinal) FROM versions latest "
            "WHERE latest.document_id = d.id) AND v.state = 'tombstone' "
            "ORDER BY v.created_at, d.id",
            (project_id,),
        )

    def create_snapshot(self, project_id: str) -> str:
        snapshot_id = str(uuid.uuid4())
        self._submit("create_snapshot", {"project_id": project_id, "snapshot_id": snapshot_id})
        return snapshot_id

    def job(self, job_id: int) -> dict[str, Any]:
        """Read durable job without exposing queue mutation."""
        row = self._queue.job(job_id)
        row["payload_document_id"] = _payload_document_id(str(row["payload"]))
        return row

    def project(self, project_id: str) -> dict[str, Any]:
        return read_row(self._database, "SELECT * FROM projects WHERE id = ?", (project_id,))

    def document_versions(self, document_id: str) -> list[dict[str, Any]]:
        return read_rows(self._database, "SELECT * FROM versions WHERE document_id = ? ORDER BY ordinal", (document_id,))

    def count(self, table: str) -> int:
        if table not in {
            "originals", "documents", "versions", "jobs", "snapshots",
            "audit", "chunks", "sources", "governance",
            "import_documents", "import_provenance", "import_receipts",
            "feedback_events", "global_sync",
        }:
            raise ValueError("unsupported table")
        return read_value(self._database, f"SELECT COUNT(*) FROM {table}")

    def active_snapshot(self, project_id: str) -> dict[str, Any] | None:
        rows = read_rows(self._database, "SELECT s.* FROM active_snapshots a JOIN snapshots s ON s.id = a.snapshot_id WHERE a.project_id = ?", (project_id,))
        return rows[0] if rows else None

    def read_active_snapshot(self, project_id: str) -> dict[str, Any] | None:
        """Read pointer and membership from one consistent DB generation."""
        with connection(self._database) as current:
            current.execute("BEGIN")
            row = current.execute("SELECT s.* FROM active_snapshots a JOIN snapshots s ON s.id = a.snapshot_id WHERE a.project_id = ?", (project_id,)).fetchone()
            if row is None:
                current.commit()
                return None
            members = current.execute("SELECT document_id, version_id FROM snapshot_versions WHERE snapshot_id = ? ORDER BY document_id", (row["id"],)).fetchall()
            current.commit()
        return {"snapshot": dict(row), "versions": [dict(member) for member in members]}

    def search_capabilities(self) -> dict[str, str]:
        return {"fts": "NOT_IMPLEMENTED", "lancedb": "NOT_IMPLEMENTED"}

    def _submit(self, kind: str, payload: dict[str, Any], key: str | None = None) -> int:
        with self._writer:
            job_id = self._queue.submit(kind, payload, key)
            self._queue.run_until(job_id, apply)
            return job_id

    @staticmethod
    def _key(prefix: str, payload: dict[str, Any]) -> str:
        canonical = json.dumps(
            {"kind": prefix, "payload": payload},
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        )
        encoded = canonical.encode("utf-8")
        return f"{prefix}:{hashlib.sha256(encoded).hexdigest()}"


def _payload_document_id(payload: str) -> str | None:
    """Expose dedupe result document ID without queue mutation capability."""
    value = json.loads(payload).get("document_id")
    return str(value) if value is not None else None
