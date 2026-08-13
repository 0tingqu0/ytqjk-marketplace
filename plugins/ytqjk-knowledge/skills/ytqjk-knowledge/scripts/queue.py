"""Private durable FIFO writer queue with lease recovery."""

from __future__ import annotations

import json
import sqlite3
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Callable

from .database import read_row, transaction
from .domain import validate_operation


LEASE_SECONDS = 30


def now() -> str:
    return datetime.now(timezone.utc).isoformat()


class _WriteQueue:
    """Queue operations only after service-level validation."""

    def __init__(self, database: Path) -> None:
        self._database = database
        self._owner = str(uuid.uuid4())

    def submit(self, kind: str, payload: dict[str, Any], key: str | None = None) -> int:
        """Persist one validated operation, returning deduplicated job id."""
        validate_operation(kind, payload)
        encoded = json.dumps(payload, sort_keys=True, separators=(",", ":"))
        with transaction(self._database) as connection:
            connection.execute(
                "INSERT OR IGNORE INTO jobs(kind, payload, state, dedupe_key, created_at) "
                "VALUES (?, ?, 'QUEUED', ?, ?)", (kind, encoded, key, now())
            )
            if key is None:
                row = connection.execute("SELECT last_insert_rowid()").fetchone()
            else:
                row = connection.execute("SELECT id FROM jobs WHERE dedupe_key = ?", (key,)).fetchone()
            return int(row[0])

    def run_until(self, job_id: int, apply: Callable[[sqlite3.Connection, str, object, str], None]) -> None:
        """Drain strictly FIFO jobs until requested job reaches a terminal state."""
        while True:
            job = self.job(job_id)
            if job["state"] == "SUCCEEDED":
                return
            if job["state"] == "FAILED":
                raise RuntimeError(str(job["error"]))
            if not self.run_next(apply):
                continue

    def run_next(self, apply: Callable[[sqlite3.Connection, str, object, str], None]) -> bool:
        """Claim then execute earliest eligible job inside one domain transaction."""
        job = self._claim()
        if job is None:
            return False
        attempt = int(job["attempt"])
        try:
            with transaction(self._database) as connection:
                self._heartbeat(connection, int(job["id"]), attempt)
                apply(connection, str(job["kind"]), json.loads(str(job["payload"])), now())
                self._finish(connection, int(job["id"]), attempt, "SUCCEEDED", None)
        except BaseException as error:
            with transaction(self._database) as connection:
                self._finish(
                    connection,
                    int(job["id"]),
                    attempt,
                    "FAILED",
                    f"{type(error).__name__}: {error}",
                )
            return False
        return True

    def job(self, job_id: int) -> dict[str, Any]:
        return read_row(self._database, "SELECT * FROM jobs WHERE id = ?", (job_id,))

    def _claim(self) -> dict[str, Any] | None:
        with transaction(self._database) as connection:
            self._recover(connection)
            row = connection.execute(
                "SELECT * FROM jobs WHERE state = 'QUEUED' AND NOT EXISTS "
                "(SELECT 1 FROM jobs WHERE state = 'RUNNING') ORDER BY id LIMIT 1"
            ).fetchone()
            if row is None:
                return None
            timestamp = now()
            lease = _lease(timestamp)
            connection.execute(
                "UPDATE jobs SET state = 'RUNNING', owner = ?, heartbeat_at = ?, "
                "lease_expires_at = ?, started_at = ?, attempt = attempt + 1 WHERE id = ?",
                (self._owner, timestamp, lease, timestamp, row["id"]),
            )
            claimed = connection.execute(
                "SELECT * FROM jobs WHERE id = ?", (row["id"],)
            ).fetchone()
            return dict(claimed)

    def _recover(self, connection: sqlite3.Connection) -> None:
        connection.execute(
            "UPDATE jobs SET state = 'QUEUED', owner = NULL, heartbeat_at = NULL, "
            "lease_expires_at = NULL WHERE state = 'RUNNING' AND lease_expires_at <= ?",
            (now(),),
        )

    def _heartbeat(
        self, connection: sqlite3.Connection, job_id: int, attempt: int
    ) -> None:
        timestamp = now()
        cursor = connection.execute(
            "UPDATE jobs SET heartbeat_at = ?, lease_expires_at = ? WHERE id = ? "
            "AND state = 'RUNNING' AND owner = ? AND attempt = ? "
            "AND lease_expires_at > ?",
            (timestamp, _lease(timestamp), job_id, self._owner, attempt, timestamp),
        )
        if cursor.rowcount != 1:
            raise RuntimeError("job lease lost")

    def _finish(
        self,
        connection: sqlite3.Connection,
        job_id: int,
        attempt: int,
        state: str,
        error: str | None,
    ) -> None:
        timestamp = now()
        cursor = connection.execute(
            "UPDATE jobs SET state = ?, error = ?, finished_at = ? WHERE id = ? "
            "AND state = 'RUNNING' AND owner = ? AND attempt = ? "
            "AND lease_expires_at > ?",
            (state, error, timestamp, job_id, self._owner, attempt, timestamp),
        )
        if cursor.rowcount != 1:
            raise RuntimeError("job lease lost")


def _lease(timestamp: str) -> str:
    value = datetime.fromisoformat(timestamp) + timedelta(seconds=LEASE_SECONDS)
    return value.isoformat()
