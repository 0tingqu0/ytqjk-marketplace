"""Validation and authentication contract for LAN knowledge peers."""

from __future__ import annotations

import base64
import hashlib
import hmac
import ipaddress
import re
import secrets
import time
from dataclasses import dataclass
from urllib.parse import urlparse


MAX_CLOCK_SKEW = 120
MAX_BODY_BYTES = 64 * 1024
_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
_NONCE = re.compile(r"^[A-Za-z0-9_-]{22,64}$")


class PeerContractError(ValueError):
    """Peer request or configuration is invalid."""


@dataclass(frozen=True, slots=True)
class PeerRecord:
    peer_id: str
    title: str
    project_id: str
    endpoint: str
    secret: str
    remote_node_id: str | None
    allow_insecure: bool = False
    enabled: bool = True
    export_node_id: str | None = None
    export_node_ids: tuple[str, ...] | None = None

    def __post_init__(self) -> None:
        identifier("peer_id", self.peer_id)
        identifier("project_id", self.project_id)
        if self.remote_node_id is not None:
            identifier("remote_node_id", self.remote_node_id)
        export_node_ids = self.export_node_ids
        if export_node_ids is None:
            export_node_ids = (
                self.project_id
                if self.export_node_id is None
                else self.export_node_id,
            )
        if (
            type(export_node_ids) is not tuple
            or not 1 <= len(export_node_ids) <= 64
        ):
            raise PeerContractError("INVALID_EXPORT_NODE_IDS")
        for export_node_id in export_node_ids:
            identifier("export_node_id", export_node_id)
        if len(set(export_node_ids)) != len(export_node_ids):
            raise PeerContractError("DUPLICATE_EXPORT_NODE")
        if (
            self.export_node_id is not None
            and self.export_node_id != export_node_ids[0]
        ):
            raise PeerContractError("PEER_EXPORT_NODES_CONFLICT")
        object.__setattr__(self, "export_node_id", export_node_ids[0])
        object.__setattr__(self, "export_node_ids", export_node_ids)
        text("title", self.title, 100)
        endpoint(self.endpoint, self.allow_insecure)
        secret_bytes(self.secret)
        if type(self.enabled) is not bool:
            raise PeerContractError("INVALID_PEER_ENABLED")

    def public(self) -> dict[str, object]:
        fingerprint = hashlib.sha256(secret_bytes(self.secret)).hexdigest()
        return {
            "peer_id": self.peer_id,
            "title": self.title,
            "project_id": self.project_id,
            "endpoint": self.endpoint,
            "remote_node_id": self.remote_node_id,
            "export_node_id": self.export_node_id,
            "export_node_ids": list(self.export_node_ids or ()),
            "allow_insecure": self.allow_insecure,
            "enabled": self.enabled,
            "key_fingerprint": fingerprint[:16],
        }


def identifier(name: str, value: object) -> str:
    if type(value) is not str or _IDENTIFIER.fullmatch(value) is None:
        raise PeerContractError(f"INVALID_{name.upper()}")
    return value


def text(name: str, value: object, limit: int) -> str:
    valid = type(value) is str and value == value.strip()
    valid = valid and 0 < len(value) <= limit
    valid = valid and not any(ord(char) < 32 for char in value)
    if not valid:
        raise PeerContractError(f"INVALID_{name.upper()}")
    return value


def endpoint(value: object, allow_insecure: object) -> str:
    if type(allow_insecure) is not bool:
        raise PeerContractError("INVALID_ALLOW_INSECURE")
    if type(value) is not str or value != value.strip():
        raise PeerContractError("INVALID_PEER_ENDPOINT")
    parsed = urlparse(value)
    try:
        port = parsed.port
    except ValueError as error:
        raise PeerContractError("INVALID_PEER_ENDPOINT") from error
    valid = (
        parsed.scheme in {"http", "https"}
        and parsed.hostname is not None
        and port is not None
        and 1 <= port <= 65535
        and parsed.path in {"", "/"}
        and not parsed.params
        and not parsed.query
        and not parsed.fragment
        and parsed.username is None
        and parsed.password is None
    )
    if not valid:
        raise PeerContractError("INVALID_PEER_ENDPOINT")
    host = parsed.hostname
    if host.casefold() != "localhost":
        try:
            address = ipaddress.ip_address(host)
        except ValueError as error:
            raise PeerContractError("PEER_IP_LITERAL_REQUIRED") from error
        if not (
            address.is_private or address.is_loopback or address.is_link_local
        ) or address.is_multicast or address.is_unspecified:
            raise PeerContractError("PEER_ENDPOINT_NOT_PRIVATE")
    if parsed.scheme == "http" and not (
        allow_insecure or is_loopback_host(host)
    ):
        raise PeerContractError("INSECURE_PEER_ENDPOINT")
    return value.rstrip("/")


def is_loopback_host(value: str) -> bool:
    if value.casefold() == "localhost":
        return True
    try:
        return ipaddress.ip_address(value).is_loopback
    except ValueError:
        return False


def secret_bytes(value: object) -> bytes:
    if type(value) is not str or not 40 <= len(value) <= 64:
        raise PeerContractError("INVALID_PEER_SECRET")
    try:
        padded = value + "=" * (-len(value) % 4)
        decoded = base64.urlsafe_b64decode(padded.encode("ascii"))
    except (UnicodeError, ValueError) as error:
        raise PeerContractError("INVALID_PEER_SECRET") from error
    if len(decoded) != 32:
        raise PeerContractError("INVALID_PEER_SECRET")
    return decoded


def new_secret() -> str:
    return base64.urlsafe_b64encode(secrets.token_bytes(32)).decode(
        "ascii"
    ).rstrip("=")


def request_nonce(value: object) -> str:
    if type(value) is not str or _NONCE.fullmatch(value) is None:
        raise PeerContractError("INVALID_PEER_AUTH")
    return value


def signed_headers(
    peer_id: str,
    secret: str,
    method: str,
    path: str,
    body: bytes,
    *,
    now: int | None = None,
    nonce: str | None = None,
) -> dict[str, str]:
    identifier("peer_id", peer_id)
    timestamp = int(time.time()) if now is None else now
    auth_nonce = nonce or secrets.token_urlsafe(18)
    if type(timestamp) is not int:
        raise PeerContractError("INVALID_PEER_AUTH")
    request_nonce(auth_nonce)
    signature = _signature(
        secret, peer_id, method, path, body, timestamp, auth_nonce
    )
    return {
        "X-YTQJK-Peer": peer_id,
        "X-YTQJK-Timestamp": str(timestamp),
        "X-YTQJK-Nonce": auth_nonce,
        "X-YTQJK-Signature": signature,
    }


def verify_signature(
    headers: object,
    secret: str,
    method: str,
    path: str,
    body: bytes,
    *,
    now: int | None = None,
) -> tuple[str, str, int]:
    try:
        peer_id = headers.get("X-YTQJK-Peer", "")
        nonce = headers.get("X-YTQJK-Nonce", "")
        raw_time = headers.get("X-YTQJK-Timestamp", "")
        supplied = headers.get("X-YTQJK-Signature", "")
    except AttributeError as error:
        raise PeerContractError("PEER_AUTH_REQUIRED") from error
    identifier("peer_id", peer_id)
    request_nonce(nonce)
    try:
        timestamp = int(raw_time)
    except (TypeError, ValueError) as error:
        raise PeerContractError("INVALID_PEER_AUTH") from error
    current = int(time.time()) if now is None else now
    if abs(current - timestamp) > MAX_CLOCK_SKEW:
        raise PeerContractError("PEER_AUTH_EXPIRED")
    expected = _signature(
        secret, peer_id, method, path, body, timestamp, nonce
    )
    if type(supplied) is not str or not hmac.compare_digest(
        supplied, expected
    ):
        raise PeerContractError("PEER_AUTH_INVALID")
    return peer_id, nonce, timestamp


def _signature(
    secret: str,
    peer_id: str,
    method: str,
    path: str,
    body: bytes,
    timestamp: int,
    nonce: str,
) -> str:
    digest = hashlib.sha256(body).hexdigest()
    message = "\n".join((
        "ytqjk-peer-v1", peer_id, method.upper(), path,
        str(timestamp), nonce, digest,
    )).encode("utf-8")
    return hmac.new(secret_bytes(secret), message, hashlib.sha256).hexdigest()


__all__ = [
    "MAX_BODY_BYTES",
    "MAX_CLOCK_SKEW",
    "PeerContractError",
    "PeerRecord",
    "endpoint",
    "identifier",
    "is_loopback_host",
    "new_secret",
    "request_nonce",
    "signed_headers",
    "verify_signature",
]
