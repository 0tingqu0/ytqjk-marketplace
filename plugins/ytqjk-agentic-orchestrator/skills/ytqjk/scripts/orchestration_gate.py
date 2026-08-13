"""One-time mutation gate for session-bound YTQJK attestations."""

from __future__ import annotations

import sqlite3

from orchestration_key import LocalHmacKey
from orchestration_models import (
    Attestation,
    GateDecision,
    IdentityError,
    LedgerUnavailable,
    LeaseState,
    binding_hash,
    now,
    public_role_name,
    validate_role,
)
from orchestration_policy import validate_grant
from orchestration_runtime import audit, verify_token
from orchestration_storage import SqliteLedger


def gate_mutation(
    ledger: SqliteLedger,
    key: LocalHmacKey,
    token: Attestation,
    current_session_key: str | None,
) -> GateDecision:
    """Fail closed, persist expiry, and consume valid mutation once."""
    try:
        with ledger.transaction() as conn:
            verify_token(ledger, key, conn, token, current_session_key)
            grant = conn.execute(
                "SELECT * FROM role_ledger WHERE run_id = ? AND session_key = ? "
                "AND role = ?",
                (token.run_id, token.session_key, token.role),
            ).fetchone()
            validate_grant(
                grant,
                validate_role(token.role),
                token.read_scope,
                token.write_scope,
                token.mutation,
            )
            if not token.mutation:
                return _denied()
            ledger.require_running(conn, token.run_id)
            lease = ledger.lease_state(conn, token.lease_id)
            version, state, expires_at, binding, run_id, session_key, role = lease
            if (run_id, session_key, role) != (
                token.run_id,
                token.session_key,
                token.role,
            ) or binding != binding_hash(token):
                return _denied()
            if state == LeaseState.ACTIVE.value and expires_at <= now():
                ledger.append_lease(
                    conn,
                    token.lease_id,
                    run_id,
                    session_key,
                    role,
                    version + 1,
                    LeaseState.EXPIRED,
                    expires_at,
                    binding,
                    now(),
                )
                audit(
                    ledger, conn, "lease_expired", run_id, session_key,
                    token.lease_id, {},
                )
                return _denied()
            if state != LeaseState.ACTIVE.value or expires_at != token.expires_at:
                return _denied()
            ledger.append_lease(
                conn, token.lease_id, run_id, session_key, role, version + 1,
                LeaseState.CONSUMED, expires_at, binding, now(),
            )
            audit(
                ledger, conn, "mutation_authorized", run_id, session_key,
                token.lease_id, {"role": token.role},
            )
        return GateDecision(
            "AUTHORIZED", "mutation authorized", public_role_name(token.role)
        )
    except LedgerUnavailable:
        return GateDecision("BLOCKED", "identity ledger or gate unavailable")
    except (IdentityError, ValueError):
        return _denied()
    except (OSError, sqlite3.Error):
        return GateDecision("BLOCKED", "identity ledger or gate unavailable")


def _denied() -> GateDecision:
    return GateDecision("DENIED", "mutation denied")
