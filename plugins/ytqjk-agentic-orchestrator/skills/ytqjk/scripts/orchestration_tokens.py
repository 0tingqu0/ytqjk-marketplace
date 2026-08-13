"""Derived HMAC attestation construction for YTQJK identity control-plane."""

from __future__ import annotations

import hmac
import secrets
from dataclasses import replace
from typing import Any, Mapping

from orchestration_models import Attestation, Role, canonical_json, now


def create_token(
    key: bytes,
    run_id: str,
    session_key: str,
    run: Mapping[str, Any],
    role: Role,
    reads: tuple[str, ...],
    writes: tuple[str, ...],
    mutation: bool,
    staged_hash: str,
    seconds: int,
) -> Attestation:
    """Create lease token bound to run, session, scopes, mutation, and DB."""
    unsigned = Attestation(
        run_id,
        session_key,
        str(run["project_id"]),
        str(run["objective_hash"]),
        role.value,
        reads,
        writes,
        mutation,
        staged_hash,
        str(run["database_id"]),
        secrets.token_urlsafe(18),
        now() + seconds,
        "",
    )
    return replace(unsigned, signature=sign_token(key, unsigned))


def sign_token(key: bytes, token: Attestation) -> str:
    """Derive run and one-time lease keys before signing full claims."""
    claims = {
        "database_id": token.database_id,
        "objective_hash": token.objective_hash,
        "project_id": token.project_id,
        "run_id": token.run_id,
        "session_key": token.session_key,
    }
    run_key = hmac.digest(key, canonical_json(claims).encode(), "sha256")
    lease_key = hmac.digest(run_key, token.lease_id.encode(), "sha256")
    return hmac.digest(
        lease_key, canonical_json(token.claims()).encode(), "sha256"
    ).hex()
