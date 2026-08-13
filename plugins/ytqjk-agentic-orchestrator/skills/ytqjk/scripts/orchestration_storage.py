"""SQLite append-only storage boundary for YTQJK identity control-plane."""

from __future__ import annotations

import secrets
import sqlite3
from contextlib import closing
from pathlib import Path

from orchestration_models import IdentityError, LedgerUnavailable, LeaseState, RunState
from orchestration_schema import SCHEMA


class SqliteLedger:
    """Own database lifecycle and append-only low-level operations."""

    def __init__(self, database_path: Path):
        self.database_path = Path(database_path)

    def initialize(self) -> str:
        """Create immutable schema and stable database identity."""
        try:
            self.database_path.parent.mkdir(parents=True, exist_ok=True)
            with closing(self._connect()) as conn:
                conn.executescript(SCHEMA)
                self._validate_schema(conn)
                row = conn.execute(
                    "SELECT value FROM metadata WHERE key = 'database_id'"
                ).fetchone()
                if row is None:
                    database_id = secrets.token_hex(16)
                    conn.execute(
                        "INSERT INTO metadata(key, value) VALUES ('database_id', ?)",
                        (database_id,),
                    )
                    return database_id
                return str(row["value"])
        except (OSError, sqlite3.Error) as error:
            raise LedgerUnavailable("身份账本不可用。") from error

    def transaction(self) -> "Transaction":
        return Transaction(self._connect())

    def database_id(self, conn: sqlite3.Connection) -> str:
        row = conn.execute(
            "SELECT value FROM metadata WHERE key = 'database_id'"
        ).fetchone()
        if row is None:
            raise LedgerUnavailable("身份账本未初始化。")
        return str(row["value"])

    def run_state(self, conn: sqlite3.Connection, run_id: str) -> tuple[int, str]:
        row = conn.execute(
            "SELECT version, state FROM run_events WHERE run_id = ? "
            "ORDER BY version DESC LIMIT 1",
            (run_id,),
        ).fetchone()
        if row is None:
            raise IdentityError("运行不存在。")
        return int(row["version"]), str(row["state"])

    def require_running(self, conn: sqlite3.Connection, run_id: str) -> None:
        _, state = self.run_state(conn, run_id)
        if state != RunState.RUNNING.value:
            raise IdentityError("运行非 RUNNING，变更关闭。")

    def lease_state(
        self, conn: sqlite3.Connection, lease_id: str
    ) -> tuple[int, str, int, str, str, str, str]:
        row = conn.execute(
            "SELECT version, state, expires_at, binding_hash, run_id, session_key, role "
            "FROM lease_events WHERE lease_id = ? ORDER BY version DESC LIMIT 1",
            (lease_id,),
        ).fetchone()
        if row is None:
            raise IdentityError("租约不存在。")
        return (
            int(row["version"]),
            str(row["state"]),
            int(row["expires_at"]),
            str(row["binding_hash"]),
            str(row["run_id"]),
            str(row["session_key"]),
            str(row["role"]),
        )

    def active_leases(
        self, conn: sqlite3.Connection, run_id: str
    ) -> list[tuple[str, int, int, str, str, str]]:
        rows = conn.execute(
            "SELECT current.lease_id, current.version, current.expires_at, "
            "current.binding_hash, current.session_key, current.role "
            "FROM lease_events current JOIN (SELECT lease_id, MAX(version) version "
            "FROM lease_events WHERE run_id = ? GROUP BY lease_id) latest "
            "ON current.lease_id = latest.lease_id AND current.version = latest.version "
            "WHERE current.run_id = ? AND current.state = 'ACTIVE'",
            (run_id, run_id),
        ).fetchall()
        return [
            (
                str(row["lease_id"]),
                int(row["version"]),
                int(row["expires_at"]),
                str(row["binding_hash"]),
                str(row["session_key"]),
                str(row["role"]),
            )
            for row in rows
        ]

    def append_run(
        self, conn: sqlite3.Connection, run_id: str, version: int, state: RunState, timestamp: int
    ) -> None:
        conn.execute(
            "INSERT INTO run_events(run_id, version, state, created_at) "
            "VALUES (?, ?, ?, ?)",
            (run_id, version, state.value, timestamp),
        )

    def append_lease(
        self,
        conn: sqlite3.Connection,
        lease_id: str,
        run_id: str,
        session_key: str,
        role: str,
        version: int,
        state: LeaseState,
        expires_at: int,
        binding: str,
        timestamp: int,
    ) -> None:
        conn.execute(
            "INSERT INTO lease_events(lease_id, run_id, session_key, role, version, "
            "state, expires_at, binding_hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            (
                lease_id,
                run_id,
                session_key,
                role,
                version,
                state.value,
                expires_at,
                binding,
                timestamp,
            ),
        )

    def audit(
        self,
        conn: sqlite3.Connection,
        kind: str,
        run_id: str,
        session_key: str,
        lease_id: str | None,
        detail: str,
        timestamp: int,
    ) -> None:
        conn.execute(
            "INSERT INTO audit_events(created_at, kind, run_id, session_key, lease_id, "
            "detail) VALUES (?, ?, ?, ?, ?, ?)",
            (timestamp, kind, run_id, session_key, lease_id, detail),
        )

    def _validate_schema(self, conn: sqlite3.Connection) -> None:
        required = {
            "runs": {"run_id", "session_key", "project_id", "objective_hash"},
            "role_ledger": {"run_id", "session_key", "role", "capabilities"},
            "lease_events": {"run_id", "session_key", "role", "state"},
            "audit_events": {"run_id", "session_key", "kind"},
        }
        for table, columns in required.items():
            actual = {str(row[1]) for row in conn.execute(f"PRAGMA table_info({table})")}
            if not columns.issubset(actual):
                raise sqlite3.DatabaseError("identity schema mismatch")

    def _connect(self) -> sqlite3.Connection:
        conn = sqlite3.connect(self.database_path, timeout=5, isolation_level=None)
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA foreign_keys = ON")
        return conn


class Transaction:
    """SQLite IMMEDIATE transaction closed on every outcome."""

    def __init__(self, conn: sqlite3.Connection):
        self.conn = conn

    def __enter__(self) -> sqlite3.Connection:
        try:
            self.conn.execute("BEGIN IMMEDIATE")
            return self.conn
        except BaseException:
            self.conn.close()
            raise

    def __exit__(self, exc_type: object, *_: object) -> None:
        try:
            if exc_type:
                self.conn.rollback()
            else:
                self.conn.commit()
        finally:
            self.conn.close()
