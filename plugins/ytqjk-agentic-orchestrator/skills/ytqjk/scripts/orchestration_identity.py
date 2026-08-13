"""YTQJK identity API: callers provide anonymous SHA-256 session_key only."""

from __future__ import annotations

import secrets
import sqlite3
from pathlib import Path
from typing import Iterable

from orchestration_key import LocalHmacKey
from orchestration_models import (
    DEFAULT_LEASE_SECONDS,
    Attestation,
    CasConflict,
    GateDecision,
    IdentityError,
    LedgerUnavailable,
    LeaseState,
    Role,
    RunState,
    binding_hash,
    canonical_capabilities,
    canonical_json,
    canonical_scope,
    now,
    validate_hash,
    validate_lease_seconds,
    validate_project,
    validate_role,
    validate_transition,
)
from orchestration_storage import SqliteLedger
from orchestration_gate import gate_mutation
from orchestration_policy import (
    require_current_session,
    require_lifecycle,
    validate_grant,
)
from orchestration_runtime import (
    audit,
    require_run_session,
    revoke_active,
    run_row,
    verify_token,
)
from orchestration_tokens import create_token


class OrchestrationControlPlane:
    """Authorize exactly one mutation from a run/session/role bound lease."""

    def __init__(self, database_path: Path, key_path: Path):
        self.ledger = SqliteLedger(database_path)
        self.key = LocalHmacKey(key_path)

    def initialize(self) -> str:
        """Create ledger and local least-privilege HMAC key."""
        database_id = self.ledger.initialize()
        self.key.read(create=True)
        return database_id

    def start_run(
        self,
        project_id: str,
        objective_hash: str,
        session_key: str,
        current_session_key: str | None = None,
    ) -> str:
        """Create immutable RUNNING run from objective/session hashes only."""
        validate_project(project_id)
        validate_hash(objective_hash, "objective_hash")
        validate_hash(session_key, "session_key")
        require_current_session(current_session_key, session_key)
        run_id = secrets.token_urlsafe(18)
        with self.ledger.transaction() as conn:
            database_id = self.ledger.database_id(conn)
            timestamp = now()
            conn.execute(
                "INSERT INTO runs(run_id, session_key, project_id, objective_hash, "
                "database_id, created_at) VALUES (?, ?, ?, ?, ?, ?)",
                (run_id, session_key, project_id, objective_hash, database_id, timestamp),
            )
            self.ledger.append_run(conn, run_id, 0, RunState.RUNNING, timestamp)
            audit(self.ledger, conn, "run_started", run_id, session_key, None, {})
        return run_id

    def register_role(
        self,
        run_id: str,
        session_key: str,
        role: str,
        read_scope: Iterable[str],
        write_scope: Iterable[str],
        mutation: bool,
        capabilities: Iterable[str] = (),
        current_session_key: str | None = None,
    ) -> None:
        """Append immutable session-bound role contract; 总监 gets no task power."""
        role_value = validate_role(role)
        reads, writes = canonical_scope(read_scope), canonical_scope(write_scope)
        capability_values = canonical_capabilities(capabilities)
        if role_value in {Role.DIRECTOR, Role.CONTROLLER} and (reads or writes or mutation):
            raise IdentityError("总监仅可协调，不能获得实现、测试、审批、知识批准或 Git 提交权限。")
        if capability_values and role_value not in {Role.DIRECTOR, Role.CONTROLLER}:
            raise IdentityError("生命周期 capability 仅供总监协调。")
        with self.ledger.transaction() as conn:
            self.ledger.require_running(conn, run_id)
            run = run_row(conn, run_id)
            require_current_session(current_session_key, str(run["session_key"]))
            require_current_session(current_session_key, session_key)
            require_run_session(conn, run_id, session_key)
            conn.execute(
                "INSERT INTO role_ledger(run_id, session_key, role, read_scope, "
                "write_scope, mutation, capabilities, created_at) "
                "VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                (run_id, session_key, role_value.value, canonical_json(reads),
                 canonical_json(writes), int(mutation),
                 canonical_json(capability_values), now()),
            )
            audit(
                self.ledger, conn, "role_registered", run_id, session_key, None,
                {"role": role_value.value},
            )

    def transition_run_cas(
        self,
        run_id: str,
        expected_version: int,
        expected_state: RunState,
        next_state: RunState,
        current_session_key: str | None = None,
    ) -> int:
        """CAS lifecycle transition; non-RUNNING target revokes all active leases."""
        validate_transition(expected_state, next_state)
        with self.ledger.transaction() as conn:
            run = run_row(conn, run_id)
            require_current_session(current_session_key, str(run["session_key"]))
            grants = conn.execute(
                "SELECT * FROM role_ledger WHERE run_id = ? AND session_key = ?",
                (run_id, current_session_key),
            ).fetchall()
            require_lifecycle(grants)
            version, state = self.ledger.run_state(conn, run_id)
            if version != expected_version or state != expected_state.value:
                raise CasConflict("运行生命周期 CAS 冲突。")
            timestamp = now()
            if next_state is not RunState.RUNNING:
                revoke_active(
                    self.ledger, conn, run_id, str(run["session_key"]), timestamp
                )
            self.ledger.append_run(conn, run_id, version + 1, next_state, timestamp)
            audit(
                self.ledger, conn, "run_transition", run_id,
                str(run["session_key"]), None, {"state": next_state.value},
            )
            return version + 1

    def issue_attestation(
        self,
        run_id: str,
        session_key: str,
        role: str,
        read_scope: Iterable[str],
        write_scope: Iterable[str],
        mutation: bool,
        staged_hash: str = "",
        lease_seconds: int = DEFAULT_LEASE_SECONDS,
        current_session_key: str | None = None,
    ) -> Attestation:
        """Issue signed lease binding run, session, role, scope, and staged hash."""
        role_value = validate_role(role)
        reads, writes = canonical_scope(read_scope), canonical_scope(write_scope)
        validate_lease_seconds(lease_seconds)
        if mutation:
            validate_hash(staged_hash, "staged_hash")
        elif staged_hash:
            raise IdentityError("非变更证明不得绑定 staged_hash。")
        with self.ledger.transaction() as conn:
            self.ledger.require_running(conn, run_id)
            stored_run = run_row(conn, run_id)
            require_current_session(
                current_session_key, str(stored_run["session_key"])
            )
            require_current_session(current_session_key, session_key)
            run = require_run_session(conn, run_id, session_key)
            grant = conn.execute(
                "SELECT * FROM role_ledger WHERE run_id = ? AND session_key = ? "
                "AND role = ?",
                (run_id, session_key, role_value.value),
            ).fetchone()
            validate_grant(grant, role_value, reads, writes, mutation)
            token = create_token(
                self.key.read(), run_id, session_key, run, role_value, reads, writes,
                mutation, staged_hash, lease_seconds,
            )
            self.ledger.append_lease(
                conn, token.lease_id, run_id, session_key, role_value.value, 0,
                LeaseState.ACTIVE, token.expires_at, binding_hash(token), now(),
            )
            audit(
                self.ledger, conn, "lease_issued", run_id, session_key,
                token.lease_id, {"role": role_value.value, "mutation": mutation},
            )
            return token

    def renew_attestation(
        self,
        token: Attestation,
        lease_seconds: int = DEFAULT_LEASE_SECONDS,
        current_session_key: str | None = None,
    ) -> Attestation:
        """Revoke prior lease, then create a fresh derived one."""
        validate_lease_seconds(lease_seconds)
        with self.ledger.transaction() as conn:
            verify_token(self.ledger, self.key, conn, token, current_session_key)
            self.ledger.require_running(conn, token.run_id)
            version, state, expires_at, binding, run_id, session_key, role = self.ledger.lease_state(
                conn, token.lease_id
            )
            if (
                (run_id, session_key, role) != (token.run_id, token.session_key, token.role)
                or state != LeaseState.ACTIVE.value
                or expires_at <= now()
                or binding != binding_hash(token)
            ):
                raise IdentityError("租约不可续期。")
            self.ledger.append_lease(
                conn, token.lease_id, run_id, session_key, role, version + 1,
                LeaseState.REVOKED, expires_at, binding, now(),
            )
            audit(
                self.ledger, conn, "lease_renewed", run_id, session_key,
                token.lease_id, {},
            )
        return self.issue_attestation(
            token.run_id, token.session_key, token.role, token.read_scope,
            token.write_scope, token.mutation, token.staged_hash, lease_seconds,
            current_session_key,
        )

    def revoke_lease(
        self,
        run_id: str,
        session_key: str,
        lease_id: str,
        current_session_key: str | None = None,
    ) -> None:
        """Append revocation when exact run/session owns current active lease."""
        with self.ledger.transaction() as conn:
            run = run_row(conn, run_id)
            require_current_session(current_session_key, str(run["session_key"]))
            require_current_session(current_session_key, session_key)
            require_run_session(conn, run_id, session_key)
            version, state, expires_at, binding, stored_run, stored_session, role = self.ledger.lease_state(
                conn, lease_id
            )
            if (stored_run, stored_session) != (run_id, session_key):
                raise IdentityError("租约不可撤销。")
            if state != LeaseState.ACTIVE.value:
                raise IdentityError("租约不可撤销。")
            self.ledger.append_lease(
                conn, lease_id, run_id, session_key, role, version + 1,
                LeaseState.REVOKED, expires_at, binding, now(),
            )
            audit(
                self.ledger, conn, "lease_revoked", run_id, session_key, lease_id, {}
            )

    def gate_mutation(
        self, token: Attestation, current_session_key: str | None = None
    ) -> GateDecision:
        """Fail closed; persist expiry before returning denial; consume only once."""
        return gate_mutation(self.ledger, self.key, token, current_session_key)

    def lease_status(
        self, lease_id: str, current_session_key: str | None = None
    ) -> LeaseState:
        """Read status, append EXPIRED/audit on observed elapsed active lease."""
        try:
            with self.ledger.transaction() as conn:
                version, state, expires_at, binding, run_id, session_key, role = self.ledger.lease_state(
                    conn, lease_id
                )
                require_current_session(current_session_key, session_key)
                if state == LeaseState.ACTIVE.value and expires_at <= now():
                    self.ledger.append_lease(
                        conn, lease_id, run_id, session_key, role, version + 1,
                        LeaseState.EXPIRED, expires_at, binding, now(),
                    )
                    audit(
                        self.ledger, conn, "lease_expired", run_id, session_key,
                        lease_id, {},
                    )
                    return LeaseState.EXPIRED
                return LeaseState(state)
        except (OSError, sqlite3.Error) as error:
            raise LedgerUnavailable("身份账本不可用。") from error
