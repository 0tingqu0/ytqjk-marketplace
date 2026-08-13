"""Types and validation shared by YTQJK identity control-plane modules."""

from __future__ import annotations

import hashlib
import json
import re
import time
from dataclasses import dataclass
from enum import Enum
from pathlib import PurePosixPath
from typing import Any, Iterable

DEFAULT_LEASE_SECONDS = 30 * 60
LIFECYCLE_CAPABILITY = "run:lifecycle"

_SENSITIVE_DIRS = {
    ".aws",
    ".azure",
    ".cargo",
    ".docker",
    ".git",
    ".gnupg",
    ".kube",
    ".m2",
    ".ssh",
    ".terraform",
    ".venv",
    "__pycache__",
    "build",
    "coverage",
    "dist",
    "node_modules",
    "vendor",
}
_SENSITIVE_NAMES = {
    ".authinfo",
    ".git-credentials",
    ".my.cnf",
    ".netrc",
    ".npmrc",
    ".pgpass",
    ".pypirc",
    ".yarnrc",
    ".yarnrc.yml",
    "_netrc",
    "auth.json",
    "credentials",
    "credentials.json",
    "credentials.toml",
    "gradle.properties",
    "id_ed25519",
    "id_rsa",
    "kubeconfig",
    "nuget.config",
    "secret.json",
    "secrets.json",
    "service-account.json",
    "service_account.json",
    "settings-security.xml",
    "token.json",
    "tokens.json",
}
_SENSITIVE_ENDINGS = (
    ".age",
    ".asc",
    ".gpg",
    ".jks",
    ".key",
    ".kdbx",
    ".keystore",
    ".p12",
    ".pem",
    ".pfx",
    ".ovpn",
    ".tfstate",
    ".tfstate.backup",
    ".tfvars",
    ".tfvars.json",
)
_SENSITIVE_CONFIG = re.compile(
    r"(?:auth|credential|credentials|secret|secrets|token|tokens)"
    r"\.(?:cfg|conf|ini|json|properties|toml|ya?ml)"
)


class IdentityError(RuntimeError):
    """Control-plane error without secret-bearing details."""


class CasConflict(IdentityError):
    """Lifecycle compare-and-swap lost."""


class LedgerUnavailable(IdentityError):
    """Ledger or local gate cannot produce a trustworthy decision."""


class RunState(str, Enum):
    RUNNING = "RUNNING"
    PAUSED = "PAUSED"
    STOPPED = "STOPPED"
    DONE = "DONE"
    BLOCKED = "BLOCKED"


class LeaseState(str, Enum):
    ACTIVE = "ACTIVE"
    CONSUMED = "CONSUMED"
    REVOKED = "REVOKED"
    EXPIRED = "EXPIRED"


class Role(str, Enum):
    DIRECTOR = "director"
    CONTROLLER = "controller"
    WORKER = "worker"
    REVIEWER = "reviewer"
    GIT = "git"


LEGAL_RUN_TRANSITIONS = {
    RunState.RUNNING: {RunState.PAUSED, RunState.STOPPED, RunState.DONE, RunState.BLOCKED},
    RunState.PAUSED: {RunState.RUNNING, RunState.STOPPED, RunState.DONE, RunState.BLOCKED},
    RunState.STOPPED: set(),
    RunState.DONE: set(),
    RunState.BLOCKED: set(),
}


@dataclass(frozen=True)
class Attestation:
    run_id: str
    session_key: str
    project_id: str
    objective_hash: str
    role: str
    read_scope: tuple[str, ...]
    write_scope: tuple[str, ...]
    mutation: bool
    staged_hash: str
    database_id: str
    lease_id: str
    expires_at: int
    signature: str

    def claims(self) -> dict[str, Any]:
        return {
            "database_id": self.database_id,
            "expires_at": self.expires_at,
            "lease_id": self.lease_id,
            "mutation": self.mutation,
            "objective_hash": self.objective_hash,
            "project_id": self.project_id,
            "read_scope": list(self.read_scope),
            "role": self.role,
            "run_id": self.run_id,
            "session_key": self.session_key,
            "staged_hash": self.staged_hash,
            "write_scope": list(self.write_scope),
        }

    def as_dict(self) -> dict[str, Any]:
        return {**self.claims(), "signature": self.signature}


@dataclass(frozen=True)
class GateDecision:
    state: str
    reason: str
    role: str | None = None


def public_role_name(role: str) -> str:
    """Keep controller internal; surface director as 总监."""
    if role in {Role.DIRECTOR.value, Role.CONTROLLER.value}:
        return "总监"
    return role


def canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def binding_hash(token: Attestation) -> str:
    return hashlib.sha256(canonical_json(token.claims()).encode()).hexdigest()


def now() -> int:
    return int(time.time())


def validate_hash(value: str, field: str = "hash") -> None:
    if len(value) != 64 or any(char not in "0123456789abcdef" for char in value):
        raise ValueError(f"{field} 必须为小写 SHA-256。")


def validate_project(value: str) -> None:
    if not value or len(value) > 128 or any(char.isspace() for char in value):
        raise ValueError("project_id 无效。")


def validate_role(value: str) -> Role:
    try:
        return Role(value)
    except ValueError as error:
        raise ValueError("角色无效。") from error


def validate_lease_seconds(value: int) -> None:
    if not 1 <= value <= DEFAULT_LEASE_SECONDS:
        raise ValueError("租约时长无效。")


def validate_transition(current: RunState, target: RunState) -> None:
    if target not in LEGAL_RUN_TRANSITIONS[current]:
        raise ValueError("非法运行状态转移。")


def canonical_scope(values: Iterable[str]) -> tuple[str, ...]:
    result = tuple(sorted(set(values)))
    for value in result:
        path = PurePosixPath(value)
        if (
            not value
            or "\\" in value
            or ":" in value
            or path.is_absolute()
            or ".." in path.parts
            or path.as_posix() != value
            or _is_sensitive_path(path)
        ):
            raise ValueError("scope 包含敏感或不安全路径。")
    return result


def _is_sensitive_path(path: PurePosixPath) -> bool:
    """Reject credential, key, repository metadata, and secret-state paths."""
    parts = {part.casefold() for part in path.parts}
    name = path.name.casefold()
    return bool(parts & _SENSITIVE_DIRS) or (
        name in _SENSITIVE_NAMES
        or _SENSITIVE_CONFIG.fullmatch(name) is not None
        or name.startswith(".env")
        or name.endswith(_SENSITIVE_ENDINGS)
    )


def canonical_capabilities(values: Iterable[str]) -> tuple[str, ...]:
    """Allow only control-plane capabilities defined by this version."""
    result = tuple(sorted(set(values)))
    if any(value != LIFECYCLE_CAPABILITY for value in result):
        raise ValueError("角色 capability 无效。")
    return result
