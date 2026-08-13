"""Runtime helpers for session-bound identity operations."""

from __future__ import annotations

import hmac
import sqlite3
from typing import Any

from orchestration_key import LocalHmacKey
from orchestration_models import (
    Attestation,
    IdentityError,
    LeaseState,
    canonical_json,
    now,
    validate_hash,
)
from orchestration_policy import require_current_session
from orchestration_storage import SqliteLedger
from orchestration_tokens import sign_token


def run_row(conn: sqlite3.Connection, run_id: str) -> sqlite3.Row:
    """Return immutable run row or fail closed."""
    row = conn.execute("SELECT * FROM runs WHERE run_id = ?", (run_id,)).fetchone()
    if row is None:
        raise IdentityError("运行不存在。")
    return row


def require_run_session(
    conn: sqlite3.Connection, run_id: str, session_key: str
) -> sqlite3.Row:
    """Require anonymous session key bound to run."""
    validate_hash(session_key, "session_key")
    row = run_row(conn, run_id)
    if not hmac.compare_digest(str(row["session_key"]), session_key):
        raise IdentityError("会话身份不匹配。")
    return row


def verify_token(
    ledger: SqliteLedger,
    key: LocalHmacKey,
    conn: sqlite3.Connection,
    token: Attestation,
    current_session_key: str | None,
) -> None:
    """Verify database, run, trusted session, and derived HMAC binding."""
    if ledger.database_id(conn) != token.database_id:
        raise IdentityError("证明签名无效。")
    require_run_session(conn, token.run_id, token.session_key)
    require_current_session(current_session_key, token.session_key)
    expected = sign_token(key.read(), token)
    if not hmac.compare_digest(expected, token.signature):
        raise IdentityError("证明签名无效。")


def audit(
    ledger: SqliteLedger,
    conn: sqlite3.Connection,
    kind: str,
    run_id: str,
    session_key: str,
    lease_id: str | None,
    detail: dict[str, Any],
) -> None:
    """Append structured audit event."""
    ledger.audit(
        conn, kind, run_id, session_key, lease_id, canonical_json(detail), now()
    )


def revoke_active(
    ledger: SqliteLedger,
    conn: sqlite3.Connection,
    run_id: str,
    session_key: str,
    timestamp: int,
) -> None:
    """Revoke every active lease before run leaves RUNNING."""
    for lease_id, version, expiry, binding, lease_session, role in ledger.active_leases(
        conn, run_id
    ):
        ledger.append_lease(
            conn,
            lease_id,
            run_id,
            lease_session,
            role,
            version + 1,
            LeaseState.REVOKED,
            expiry,
            binding,
            timestamp,
        )
        audit(
            ledger,
            conn,
            "lease_revoked",
            run_id,
            session_key,
            lease_id,
            {"reason": "run_not_running"},
        )
