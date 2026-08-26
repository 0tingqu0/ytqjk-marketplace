"""HMAC contract for successful LAN peer responses."""

from __future__ import annotations

import hashlib
import hmac
import re

from knowledge_peer_contract import (
    PeerContractError,
    identifier,
    request_nonce,
    secret_bytes,
)


RESPONSE_PEER_HEADER = "X-YTQJK-Response-Peer"
RESPONSE_SIGNATURE_HEADER = "X-YTQJK-Response-Signature"
_SIGNATURE = re.compile(r"^[0-9a-f]{64}$")


def signed_response_headers(
    peer_id: str,
    secret: str,
    status: int,
    path: str,
    nonce: str,
    body: bytes,
) -> dict[str, str]:
    _validate_parts(peer_id, status, path, nonce, body)
    signature = _signature(
        secret, peer_id, status, path, nonce, body
    )
    return {
        RESPONSE_PEER_HEADER: peer_id,
        RESPONSE_SIGNATURE_HEADER: signature,
    }


def verify_response_signature(
    headers: object,
    secret: str,
    expected_peer_id: str,
    status: int,
    path: str,
    nonce: str,
    body: bytes,
) -> str:
    try:
        peer_id = headers.get(RESPONSE_PEER_HEADER, "")
        supplied = headers.get(RESPONSE_SIGNATURE_HEADER, "")
    except AttributeError as error:
        raise PeerContractError(
            "PEER_RESPONSE_AUTH_INVALID"
        ) from error
    try:
        _validate_parts(peer_id, status, path, nonce, body)
        identifier("peer_id", expected_peer_id)
    except PeerContractError as error:
        raise PeerContractError(
            "PEER_RESPONSE_AUTH_INVALID"
        ) from error
    expected = _signature(
        secret, peer_id, status, path, nonce, body
    )
    valid = type(supplied) is str
    valid = valid and _SIGNATURE.fullmatch(supplied) is not None
    valid = valid and hmac.compare_digest(peer_id, expected_peer_id)
    valid = valid and hmac.compare_digest(supplied, expected)
    if not valid:
        raise PeerContractError("PEER_RESPONSE_AUTH_INVALID")
    return peer_id


def _validate_parts(
    peer_id: object,
    status: object,
    path: object,
    nonce: object,
    body: object,
) -> None:
    identifier("peer_id", peer_id)
    request_nonce(nonce)
    valid_status = isinstance(status, int)
    valid_status = valid_status and not isinstance(status, bool)
    valid_status = valid_status and 100 <= status <= 599
    valid_path = type(path) is str and path.startswith("/")
    valid_path = valid_path and len(path) <= 4096
    valid_path = valid_path and not any(ord(char) < 32 for char in path)
    if not valid_status or not valid_path or type(body) is not bytes:
        raise PeerContractError("INVALID_PEER_RESPONSE_AUTH")


def _signature(
    secret: str,
    peer_id: str,
    status: int,
    path: str,
    nonce: str,
    body: bytes,
) -> str:
    digest = hashlib.sha256(body).hexdigest()
    message = "\n".join((
        "ytqjk-peer-response-v1",
        peer_id,
        str(int(status)),
        path,
        nonce,
        digest,
    )).encode("utf-8")
    return hmac.new(
        secret_bytes(secret), message, hashlib.sha256
    ).hexdigest()


__all__ = [
    "RESPONSE_PEER_HEADER",
    "RESPONSE_SIGNATURE_HEADER",
    "signed_response_headers",
    "verify_response_signature",
]
