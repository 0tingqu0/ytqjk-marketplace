from __future__ import annotations

import json
import sys
from pathlib import Path
from urllib.parse import urlsplit

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_peer_client import (  # noqa: E402
    KnowledgePeerClient,
    PeerClientError,
)
from knowledge_peer_contract import (  # noqa: E402
    PeerContractError,
    PeerRecord,
    new_secret,
)
from knowledge_peer_response import (  # noqa: E402
    RESPONSE_PEER_HEADER,
    RESPONSE_SIGNATURE_HEADER,
    signed_response_headers,
    verify_response_signature,
)
from knowledge_peer_store import PeerConfigStore  # noqa: E402


PROJECT_ID = "shared-project--0123456789ab"
REMOTE_PEER = "peer-remote"
REMOTE_NODE = "remote-export"
NONCE = "N" * 22


class FakeResponse:
    def __init__(
        self,
        status: int,
        headers: dict[str, str],
        body: bytes,
    ) -> None:
        self.status = status
        self.headers = headers
        self.body = body

    def __enter__(self) -> FakeResponse:
        return self

    def __exit__(self, *_args: object) -> None:
        return None

    def read(self, _limit: int) -> bytes:
        return self.body


class FakePeerService:
    def __init__(self, secret: str, tamper: str | None = None) -> None:
        self.secret = secret
        self.tamper = tamper

    def open(
        self,
        request: object,
        timeout: float,
    ) -> FakeResponse:
        del timeout
        request_headers = {
            key.casefold(): value
            for key, value in request.header_items()
        }
        nonce = request_headers["x-ytqjk-nonce"]
        path = urlsplit(request.full_url).path
        payload: dict[str, object] = {
            "ok": True,
            "status": "READY",
            "peer_id": REMOTE_PEER,
            "project_id": PROJECT_ID,
            "export_node_id": REMOTE_NODE,
            "library_count": 2,
            "capabilities": [
                "query-v1",
                "material-v1",
                "response-hmac-v1",
            ],
        }
        self._tamper_contract(payload)
        body = json.dumps(
            payload,
            ensure_ascii=False,
            separators=(",", ":"),
            sort_keys=True,
        ).encode("utf-8")
        headers = signed_response_headers(
            REMOTE_PEER,
            self.secret,
            200,
            path,
            nonce,
            body,
        )
        status = 200
        if self.tamper == "body":
            body += b" "
        elif self.tamper == "status":
            status = 201
        elif self.tamper == "path":
            headers = self._resign("/v1/query", nonce, body)
        elif self.tamper == "nonce":
            headers = self._resign(path, "T" * 22, body)
        elif self.tamper == "peer":
            headers[RESPONSE_PEER_HEADER] = "peer-attacker"
        elif self.tamper == "signature":
            signature = headers[RESPONSE_SIGNATURE_HEADER]
            replacement = "0" if signature[-1] != "0" else "1"
            headers[RESPONSE_SIGNATURE_HEADER] = (
                signature[:-1] + replacement
            )
        return FakeResponse(status, headers, body)

    def _resign(
        self,
        path: str,
        nonce: str,
        body: bytes,
    ) -> dict[str, str]:
        return signed_response_headers(
            REMOTE_PEER,
            self.secret,
            200,
            path,
            nonce,
            body,
        )

    def _tamper_contract(self, payload: dict[str, object]) -> None:
        if self.tamper == "missing-export":
            payload.pop("export_node_id")
        elif self.tamper == "extra-field":
            payload["extra"] = True
        elif self.tamper == "export-mismatch":
            payload["export_node_id"] = "wrong-export"
        elif self.tamper == "export-invalid":
            payload["export_node_id"] = "bad export"
        elif self.tamper == "library-bool":
            payload["library_count"] = True
        elif self.tamper == "library-negative":
            payload["library_count"] = -1
        elif self.tamper == "capability-missing":
            payload["capabilities"] = ["query-v1", "material-v1"]


def _client(
    tmp_path: Path,
    tamper: str | None = None,
) -> KnowledgePeerClient:
    secret = new_secret()
    store = PeerConfigStore(tmp_path)
    settings = store.load(create=True)
    store.upsert(
        PeerRecord(
            REMOTE_PEER,
            "Remote",
            PROJECT_ID,
            "http://127.0.0.1:9",
            secret,
            REMOTE_NODE,
        ),
        expected_revision=settings.revision,
    )
    client = KnowledgePeerClient(tmp_path)
    client.opener = FakePeerService(secret, tamper)
    return client


def test_response_signature_round_trip() -> None:
    secret = new_secret()
    body = b'{"ok":true}'
    headers = signed_response_headers(
        REMOTE_PEER,
        secret,
        200,
        "/v1/health",
        NONCE,
        body,
    )

    verified = verify_response_signature(
        headers,
        secret,
        REMOTE_PEER,
        200,
        "/v1/health",
        NONCE,
        body,
    )

    assert verified == REMOTE_PEER


def test_response_signature_is_required() -> None:
    with pytest.raises(
        PeerContractError,
        match="PEER_RESPONSE_AUTH_INVALID",
    ):
        verify_response_signature(
            {},
            new_secret(),
            REMOTE_PEER,
            200,
            "/v1/health",
            NONCE,
            b"{}",
        )


def test_health_accepts_signed_exact_contract(tmp_path: Path) -> None:
    result = _client(tmp_path).health(REMOTE_PEER, PROJECT_ID)

    assert result["export_node_id"] == REMOTE_NODE
    assert result["library_count"] == 2
    assert "response-hmac-v1" in result["capabilities"]


@pytest.mark.parametrize(
    "tamper",
    ["body", "status", "path", "nonce", "peer", "signature"],
)
def test_client_rejects_response_auth_tampering(
    tmp_path: Path,
    tamper: str,
) -> None:
    with pytest.raises(PeerClientError, match="PEER_RESPONSE_INVALID"):
        _client(tmp_path, tamper).health(REMOTE_PEER, PROJECT_ID)


@pytest.mark.parametrize(
    "tamper",
    [
        "missing-export",
        "extra-field",
        "export-mismatch",
        "export-invalid",
        "library-bool",
        "library-negative",
        "capability-missing",
    ],
)
def test_health_rejects_contract_drift(
    tmp_path: Path,
    tamper: str,
) -> None:
    with pytest.raises(PeerClientError, match="PEER_RESPONSE_INVALID"):
        _client(tmp_path, tamper).health(REMOTE_PEER, PROJECT_ID)
