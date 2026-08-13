"""Fail-closed session and role policy for YTQJK control-plane APIs."""

from __future__ import annotations

import hmac
import json
from collections.abc import Iterable, Mapping
from typing import Any

from orchestration_models import (
    LIFECYCLE_CAPABILITY,
    IdentityError,
    LedgerUnavailable,
    Role,
    validate_hash,
)


_DIRECTORS = {Role.DIRECTOR.value, Role.CONTROLLER.value}


def require_current_session(current_session_key: str | None, expected: str) -> None:
    """Require trusted host session input independent from stored/token claims."""
    if current_session_key is None:
        raise LedgerUnavailable("可信当前会话缺失，操作 BLOCKED。")
    try:
        validate_hash(current_session_key, "current_session_key")
    except ValueError as error:
        raise LedgerUnavailable("可信当前会话无效，操作 BLOCKED。") from error
    if not hmac.compare_digest(current_session_key, expected):
        raise LedgerUnavailable("可信当前会话不匹配，操作 BLOCKED。")


def validate_grant(
    grant: Mapping[str, Any] | None,
    role: Role,
    reads: Iterable[str],
    writes: Iterable[str],
    mutation: bool,
) -> None:
    """Revalidate immutable grant even if DB defenses were bypassed."""
    if grant is None:
        raise IdentityError("角色账本不允许该证明。")
    grant_reads = _array(grant["read_scope"])
    grant_writes = _array(grant["write_scope"])
    capabilities = _array(grant["capabilities"])
    if role.value in _DIRECTORS and (
        grant_reads
        or grant_writes
        or bool(grant["mutation"])
        or set(capabilities) - {LIFECYCLE_CAPABILITY}
    ):
        raise LedgerUnavailable("总监权限账本异常，操作 BLOCKED。")
    if bool(grant["mutation"]) != mutation:
        raise IdentityError("角色账本不允许该证明。")
    if not set(reads).issubset(grant_reads) or not set(writes).issubset(grant_writes):
        raise IdentityError("证明 scope 超出角色账本。")


def require_lifecycle(grants: Iterable[Mapping[str, Any]]) -> None:
    """Require a coordination-only role carrying lifecycle capability."""
    for grant in grants:
        role = str(grant["role"])
        if role in _DIRECTORS:
            validate_grant(grant, Role(role), (), (), False)
            if LIFECYCLE_CAPABILITY in _array(grant["capabilities"]):
                return
    raise LedgerUnavailable("当前会话无生命周期协调权限，操作 BLOCKED。")


def _array(value: Any) -> tuple[str, ...]:
    try:
        parsed = json.loads(str(value))
    except (TypeError, ValueError) as error:
        raise LedgerUnavailable("角色账本结构无效，操作 BLOCKED。") from error
    if not isinstance(parsed, list) or any(not isinstance(item, str) for item in parsed):
        raise LedgerUnavailable("角色账本结构无效，操作 BLOCKED。")
    return tuple(parsed)
