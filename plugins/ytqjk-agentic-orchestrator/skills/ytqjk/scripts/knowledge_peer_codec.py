"""Strict persisted configuration codec for LAN knowledge peers."""

from __future__ import annotations

import hashlib
import hmac
import ipaddress
import json
from dataclasses import dataclass

from knowledge_peer_contract import PeerContractError, PeerRecord, identifier


SCHEMA_VERSION = 1


class PeerStoreError(RuntimeError):
    """Peer configuration could not be safely read or changed."""


@dataclass(frozen=True, slots=True)
class PeerSettings:
    revision: int
    local_peer_id: str
    enabled: bool
    bind_host: str
    port: int
    allow_insecure_lan: bool
    peers: tuple[PeerRecord, ...]

    def peer(self, peer_id: str) -> PeerRecord | None:
        return next(
            (item for item in self.peers if item.peer_id == peer_id),
            None,
        )

    def public(self) -> dict[str, object]:
        return {
            "schema_version": SCHEMA_VERSION,
            "revision": self.revision,
            "local_peer_id": self.local_peer_id,
            "enabled": self.enabled,
            "bind_host": self.bind_host,
            "port": self.port,
            "allow_insecure_lan": self.allow_insecure_lan,
            "peers": [item.public() for item in self.peers],
        }


def encode_settings(settings: PeerSettings) -> bytes:
    body = _body(settings)
    payload = {**body, "digest": _digest(body)}
    return json.dumps(
        payload,
        ensure_ascii=False,
        allow_nan=False,
        indent=2,
        sort_keys=True,
    ).encode("utf-8") + b"\n"


def decode_settings(content: bytes) -> PeerSettings:
    try:
        value = json.loads(
            content.decode("utf-8"),
            object_pairs_hook=_unique_object,
        )
        return _settings(value)
    except (
        UnicodeError,
        json.JSONDecodeError,
        PeerContractError,
        TypeError,
        ValueError,
    ) as error:
        raise PeerStoreError("PEER_CONFIG_INVALID") from error


def _unique_object(
    pairs: list[tuple[str, object]],
) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("DUPLICATE_CONFIG_FIELD")
        value[key] = item
    return value


def validate_local(
    enabled: object,
    bind_host: object,
    port: object,
    allow_insecure: object,
) -> None:
    if type(enabled) is not bool or type(allow_insecure) is not bool:
        raise PeerStoreError("INVALID_PEER_SERVICE_CONFIG")
    if type(bind_host) is not str or not bind_host.strip():
        raise PeerStoreError("INVALID_PEER_BIND_HOST")
    if type(port) is not int or not 1 <= port <= 65535:
        raise PeerStoreError("INVALID_PEER_PORT")
    if bind_host not in {"127.0.0.1", "::1", "0.0.0.0", "::"}:
        try:
            address = ipaddress.ip_address(bind_host)
        except ValueError as error:
            raise PeerStoreError("INVALID_PEER_BIND_HOST") from error
        if not address.is_private:
            raise PeerStoreError("PEER_BIND_NOT_PRIVATE")
    loopback = bind_host in {"127.0.0.1", "::1"}
    if enabled and not loopback and not allow_insecure:
        raise PeerStoreError("INSECURE_LAN_CONFIRMATION_REQUIRED")


def _body(settings: PeerSettings) -> dict[str, object]:
    return {
        "schema_version": SCHEMA_VERSION,
        "revision": settings.revision,
        "local": {
            "peer_id": settings.local_peer_id,
            "enabled": settings.enabled,
            "bind_host": settings.bind_host,
            "port": settings.port,
            "allow_insecure_lan": settings.allow_insecure_lan,
        },
        "peers": [
            {
                "peer_id": item.peer_id,
                "title": item.title,
                "project_id": item.project_id,
                "endpoint": item.endpoint,
                "secret": item.secret,
                "remote_node_id": item.remote_node_id,
                "export_node_id": item.export_node_id,
                "allow_insecure": item.allow_insecure,
                "enabled": item.enabled,
            }
            for item in settings.peers
        ],
    }


def _settings(value: object) -> PeerSettings:
    required = {"schema_version", "revision", "local", "peers", "digest"}
    if type(value) is not dict or set(value) != required:
        raise PeerStoreError("PEER_CONFIG_INVALID")
    body = {key: value[key] for key in required if key != "digest"}
    supplied = value["digest"]
    if type(supplied) is not str or not hmac.compare_digest(
        supplied, _digest(body)
    ):
        raise PeerStoreError("PEER_CONFIG_DIGEST_MISMATCH")
    if value["schema_version"] != SCHEMA_VERSION:
        raise PeerStoreError("PEER_CONFIG_SCHEMA_INVALID")
    revision = value["revision"]
    if type(revision) is not int or not 0 <= revision <= 2**63 - 1:
        raise PeerStoreError("PEER_CONFIG_REVISION_INVALID")
    local = value["local"]
    if type(local) is not dict or set(local) != {
        "peer_id", "enabled", "bind_host", "port",
        "allow_insecure_lan",
    }:
        raise PeerStoreError("PEER_CONFIG_INVALID")
    identifier("local_peer_id", local["peer_id"])
    peers = value["peers"]
    if type(peers) is not list or len(peers) > 256:
        raise PeerStoreError("PEER_CONFIG_INVALID")
    records = tuple(PeerRecord(**item) for item in peers)
    if len({item.peer_id for item in records}) != len(records):
        raise PeerStoreError("DUPLICATE_PEER")
    validate_local(
        local["enabled"],
        local["bind_host"],
        local["port"],
        local["allow_insecure_lan"],
    )
    return PeerSettings(
        revision,
        local["peer_id"],
        local["enabled"],
        local["bind_host"],
        local["port"],
        local["allow_insecure_lan"],
        records,
    )


def _digest(value: object) -> str:
    content = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(content).hexdigest()


__all__ = [
    "PeerSettings",
    "PeerStoreError",
    "SCHEMA_VERSION",
    "decode_settings",
    "encode_settings",
    "validate_local",
]
