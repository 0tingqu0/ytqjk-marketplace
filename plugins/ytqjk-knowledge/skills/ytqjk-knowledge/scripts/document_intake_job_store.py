"""Transactional SQLite store for fenced document-intake jobs."""

from __future__ import annotations

import math
import sqlite3
import time
import uuid
from collections.abc import Callable, Mapping
from pathlib import Path

from .database import connection, transaction
from .document_intake_jobs import (
    DOCUMENT_JOB_SCHEMA,
    NON_RETRYABLE,
    SCHEMA_VERSION,
    STAGES,
    DocumentIntakeJob,
    JobState,
    JobTransitionError,
    JobValidationError,
    LeaseLostError,
    decode_job_record,
    encode_config,
    encode_payload,
    idempotency_key,
    normalize_error,
    stage_progress,
    validate_owner,
    validate_page_count,
    validate_store_options,
)


class DocumentIntakeJobStore:
    # Callers execute stages and explicitly report their completion.

    def __init__(
        self,
        database: Path,
        *,
        lease_seconds: float = 30,
        max_attempts: int = 3,
        clock: Callable[[], float] | None = None,
    ) -> None:
        if not isinstance(database, Path) or not database.parent.is_dir():
            raise JobValidationError("database path is invalid")
        lease, attempts = validate_store_options(
            lease_seconds, max_attempts, clock
        )
        self._database = database
        self._lease_seconds = lease
        self._max_attempts = attempts
        self._clock = time.time if clock is None else clock
        self._initialize()
        self.recover_expired()

    def schema_version(self) -> int:
        with connection(self._database) as current:
            return int(current.execute("PRAGMA user_version").fetchone()[0])

    def enqueue(
        self,
        payload: Mapping[str, object],
        config: Mapping[str, object] | None = None,
    ) -> DocumentIntakeJob:
        payload_json = encode_payload(payload)
        config_json = encode_config({} if config is None else config)
        key = idempotency_key(payload_json, config_json)
        now = self._now()
        with transaction(self._database) as current:
            job_id = str(uuid.uuid4())
            current.execute(
                "INSERT INTO document_intake_jobs VALUES "
                "(?,?,'QUEUED',0,'validate',0,NULL,?,?,0,0,NULL,NULL,NULL,"
                "NULL,NULL,?,?) ON CONFLICT(idempotency_key) DO NOTHING",
                (job_id, key, payload_json, config_json, now, now),
            )
            row = current.execute(
                "SELECT * FROM document_intake_jobs "
                "WHERE idempotency_key=?", (key,),
            ).fetchone()
            same_input = row["payload_json"] == payload_json
            same_config = row["config_json"] == config_json
            if not same_input or not same_config:
                raise sqlite3.DatabaseError("idempotency digest collision")
            if row["id"] == job_id:
                self._event(current, row, "enqueue")
            return decode_job_record(row)

    def claim(self, owner: str) -> DocumentIntakeJob | None:
        worker = validate_owner(owner)
        now = self._now()
        with transaction(self._database) as current:
            self._recover_locked(current, now)
            row = current.execute(
                "SELECT * FROM document_intake_jobs WHERE state='QUEUED' "
                "AND attempt<? ORDER BY created_at,id LIMIT 1",
                (self._max_attempts,),
            ).fetchone()
            if row is None:
                return None
            return self._update(
                current, row, "claim", state="RUNNING", owner=worker,
                attempt=int(row["attempt"]) + 1, heartbeat_at=now,
                lease_expires_at=now + self._lease_seconds,
                error_category=None, error_ref=None,
            )

    def heartbeat(
        self, job_id: str, owner: str, attempt: int
    ) -> DocumentIntakeJob:
        now = self._now()
        with transaction(self._database) as current:
            row = self._leased(current, job_id, owner, attempt, now)
            return self._update(
                current, row, "heartbeat", heartbeat_at=now,
                lease_expires_at=now + self._lease_seconds,
            )

    def advance(
        self,
        job_id: str,
        owner: str,
        attempt: int,
        stage: str,
        *,
        page_count: int | None = None,
    ) -> DocumentIntakeJob:
        now = self._now()
        with transaction(self._database) as current:
            row = self._leased(current, job_id, owner, attempt, now)
            if row["stage"] != stage or stage == "complete":
                raise JobTransitionError("completed stage is not current")
            pages = row["page_count"]
            if stage == "page-detect":
                pages = validate_page_count(page_count)
            elif page_count is not None:
                raise JobValidationError("page count belongs to page-detect")
            next_index = int(row["stage_index"]) + 1
            progress = stage_progress(next_index, pages)
            if progress < int(row["progress"]):
                raise sqlite3.DatabaseError("job progress would decrease")
            return self._update(
                current, row, f"advance:{stage}",
                stage_index=next_index, stage=STAGES[next_index],
                page_count=pages, progress=progress,
            )

    def complete(
        self, job_id: str, owner: str, attempt: int
    ) -> DocumentIntakeJob:
        now = self._now()
        with transaction(self._database) as current:
            row = self._leased(current, job_id, owner, attempt, now)
            if row["stage"] != "complete":
                raise JobTransitionError("candidate-write is not complete")
            return self._update(
                current, row, "complete", state="SUCCEEDED", progress=100,
                owner=None, heartbeat_at=None, lease_expires_at=None,
            )

    def fail(
        self,
        job_id: str,
        owner: str,
        attempt: int,
        category: str,
        detail: str,
    ) -> DocumentIntakeJob:
        error_category, error_ref = normalize_error(category, detail)
        now = self._now()
        with transaction(self._database) as current:
            row = self._leased(current, job_id, owner, attempt, now)
            return self._update(
                current, row, "fail", state="FAILED", owner=None,
                heartbeat_at=None, lease_expires_at=None,
                error_category=error_category, error_ref=error_ref,
            )

    def retry(self, job_id: str) -> DocumentIntakeJob:
        with transaction(self._database) as current:
            row = self._fetch(current, job_id)
            if row["state"] != "FAILED":
                raise JobTransitionError("only failed jobs can retry")
            if int(row["attempt"]) >= self._max_attempts:
                raise JobTransitionError("retry limit reached")
            if row["error_category"] in NON_RETRYABLE:
                raise JobTransitionError("failure is not retryable")
            return self._update(
                current, row, "retry", state="QUEUED",
                error_category=None, error_ref=None,
            )

    def cancel(self, job_id: str) -> DocumentIntakeJob:
        with transaction(self._database) as current:
            row = self._fetch(current, job_id)
            if row["state"] not in ("QUEUED", "RUNNING"):
                raise JobTransitionError("job cannot be cancelled")
            return self._update(
                current, row, "cancel", state="CANCELLED", owner=None,
                heartbeat_at=None, lease_expires_at=None,
            )

    def get(self, job_id: str) -> DocumentIntakeJob:
        with connection(self._database) as current:
            return decode_job_record(self._fetch(current, job_id))

    def list(
        self, state: JobState | None = None, *, limit: int = 100
    ) -> tuple[DocumentIntakeJob, ...]:
        if isinstance(limit, bool) or not 1 <= limit <= 1_000:
            raise JobValidationError("list limit is invalid")
        query = "SELECT * FROM document_intake_jobs"
        params: tuple[object, ...] = ()
        if state is not None:
            if not isinstance(state, JobState):
                raise JobValidationError("list state is invalid")
            query += " WHERE state=?"
            params = (state.value,)
        query += " ORDER BY created_at,id LIMIT ?"
        with connection(self._database) as current:
            rows = current.execute(query, params + (limit,)).fetchall()
            return tuple(decode_job_record(row) for row in rows)

    def recover_expired(self) -> int:
        with transaction(self._database) as current:
            return self._recover_locked(current, self._now())

    def _initialize(self) -> None:
        with transaction(self._database) as current:
            version = int(current.execute("PRAGMA user_version").fetchone()[0])
            if version not in (0, SCHEMA_VERSION):
                raise sqlite3.DatabaseError("document intake schema mismatch")
            if version == 0:
                for statement in DOCUMENT_JOB_SCHEMA:
                    current.execute(statement)
                current.execute(f"PRAGMA user_version={SCHEMA_VERSION}")
            issues = current.execute("PRAGMA foreign_key_check").fetchall()
            if issues:
                raise sqlite3.DatabaseError("document intake foreign key error")

    def _recover_locked(
        self, current: sqlite3.Connection, now: float
    ) -> int:
        rows = current.execute(
            "SELECT * FROM document_intake_jobs WHERE state='RUNNING' "
            "AND lease_expires_at<=? ORDER BY created_at,id", (now,),
        ).fetchall()
        for row in rows:
            retryable = int(row["attempt"]) < self._max_attempts
            detail = f"expired-attempt:{row['attempt']}"
            category, reference = normalize_error("WORKER_EXPIRED", detail)
            self._update(
                current, row, "recover-expired",
                state="QUEUED" if retryable else "FAILED", owner=None,
                heartbeat_at=None, lease_expires_at=None,
                error_category=category, error_ref=reference,
            )
        return len(rows)

    def _leased(
        self, current: sqlite3.Connection, job_id: str,
        owner: str, attempt: int, now: float,
    ) -> sqlite3.Row:
        worker = validate_owner(owner)
        row = self._fetch(current, job_id)
        valid = (
            type(attempt) is int and row["state"] == "RUNNING"
            and row["owner"] == worker and row["attempt"] == attempt
            and row["lease_expires_at"] > now
        )
        if not valid:
            raise LeaseLostError("document intake lease lost")
        return row

    def _update(
        self, current: sqlite3.Connection, row: sqlite3.Row,
        event: str, **updates: object,
    ) -> DocumentIntakeJob:
        updates["revision"] = int(row["revision"]) + 1
        updates["updated_at"] = self._now()
        fields = tuple(updates)
        assignments = ",".join(f"{field}=?" for field in fields)
        values = tuple(updates[field] for field in fields)
        cursor = current.execute(
            f"UPDATE document_intake_jobs SET {assignments} "
            "WHERE id=? AND revision=?",
            values + (row["id"], row["revision"]),
        )
        if cursor.rowcount != 1:
            raise LeaseLostError("document intake revision lost")
        saved = self._fetch(current, str(row["id"]))
        self._event(current, saved, event)
        return decode_job_record(saved)

    @staticmethod
    def _event(
        current: sqlite3.Connection, row: sqlite3.Row, event: str
    ) -> None:
        current.execute(
            "INSERT INTO document_intake_job_revisions VALUES (?,?,?,?,?,?,?)",
            (
                row["id"], row["revision"], event, row["state"],
                row["stage"], row["progress"], row["updated_at"],
            ),
        )

    @staticmethod
    def _fetch(current: sqlite3.Connection, job_id: str) -> sqlite3.Row:
        row = current.execute(
            "SELECT * FROM document_intake_jobs WHERE id=?", (job_id,),
        ).fetchone()
        if row is None:
            raise KeyError("document intake job not found")
        return row

    def _now(self) -> float:
        value = self._clock()
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            raise JobValidationError("clock value is invalid")
        current = float(value)
        if not math.isfinite(current) or current < 0:
            raise JobValidationError("clock value is invalid")
        return current


__all__ = ["DocumentIntakeJobStore"]
