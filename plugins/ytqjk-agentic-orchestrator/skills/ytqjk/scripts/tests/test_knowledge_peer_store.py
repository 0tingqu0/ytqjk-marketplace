from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from knowledge_peer_contract import (  # noqa: E402
    PeerContractError,
    PeerRecord,
    new_secret,
    signed_headers,
    verify_signature,
)
from knowledge_peer_codec import (  # noqa: E402
    decode_settings,
    encode_settings,
)
from knowledge_peer_store import (  # noqa: E402
    PeerConfigStore,
    PeerStoreError,
)


PROJECT = "project--0123456789ab"


def test_store_redacts_secrets_and_uses_revision_cas(
    tmp_path: Path,
) -> None:
    store = PeerConfigStore(tmp_path)
    initial = store.load(create=True)
    secret = new_secret()
    saved = store.upsert(
        PeerRecord(
            "peer-remote",
            "Remote",
            PROJECT,
            "http://127.0.0.1:8766",
            secret,
            PROJECT,
        ),
        expected_revision=initial.revision,
    )

    public = saved.public()
    assert public["peers"][0]["key_fingerprint"]
    assert "secret" not in public["peers"][0]
    assert secret not in json.dumps(public)
    with pytest.raises(PeerStoreError, match="PEER_REVISION_CONFLICT"):
        store.remove("peer-remote", expected_revision=initial.revision)


def test_plain_lan_requires_explicit_confirmation(tmp_path: Path) -> None:
    store = PeerConfigStore(tmp_path)
    initial = store.load(create=True)
    with pytest.raises(
        PeerStoreError,
        match="INSECURE_LAN_CONFIRMATION_REQUIRED",
    ):
        store.configure_local(
            enabled=True,
            bind_host="0.0.0.0",
            port=8766,
            allow_insecure_lan=False,
            expected_revision=initial.revision,
        )


def test_peer_endpoint_is_private_ip_and_nodes_are_directional() -> None:
    secret = new_secret()
    with pytest.raises(
        PeerContractError, match="PEER_IP_LITERAL_REQUIRED"
    ):
        PeerRecord(
            "peer-remote",
            "Remote",
            PROJECT,
            "https://example.test:8766",
            secret,
            PROJECT,
        )
    record = PeerRecord(
        "peer-remote",
        "Remote",
        PROJECT,
        "http://127.0.0.1:8766",
        secret,
        "remote-child",
        export_node_id="local-child",
    )
    assert record.remote_node_id == "remote-child"
    assert record.export_node_id == "local-child"


def test_peer_can_be_inbound_only_with_multiple_open_libraries() -> None:
    record = PeerRecord(
        "peer-remote",
        "Remote",
        PROJECT,
        "http://127.0.0.1:8766",
        new_secret(),
        None,
        export_node_ids=(PROJECT, "local-child"),
    )

    assert record.remote_node_id is None
    assert record.export_node_ids == (PROJECT, "local-child")
    assert record.public()["export_node_ids"] == [PROJECT, "local-child"]


def test_schema_one_single_export_migrates_on_read() -> None:
    secret = new_secret()
    body = {
        "schema_version": 1,
        "revision": 3,
        "local": {
            "peer_id": "peer-local",
            "enabled": False,
            "bind_host": "127.0.0.1",
            "port": 8766,
            "allow_insecure_lan": False,
        },
        "peers": [{
            "peer_id": "peer-remote",
            "title": "Remote",
            "project_id": PROJECT,
            "endpoint": "http://127.0.0.1:8766",
            "secret": secret,
            "remote_node_id": "remote-child",
            "export_node_id": "local-child",
            "allow_insecure": False,
            "enabled": True,
        }],
    }
    canonical = json.dumps(
        body,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    payload = {
        **body,
        "digest": hashlib.sha256(canonical).hexdigest(),
    }

    settings = decode_settings(json.dumps(payload).encode("utf-8"))

    assert settings.peers[0].export_node_ids == ("local-child",)
    assert json.loads(encode_settings(settings))["schema_version"] == 2


def test_config_rejects_duplicate_json_fields(tmp_path: Path) -> None:
    settings = PeerConfigStore(tmp_path).load(create=True)
    valid = encode_settings(settings).decode("utf-8")
    duplicate = valid.replace(
        '"revision": 0,',
        '"revision": 0,\n  "revision": 0,',
        1,
    ).encode("utf-8")

    with pytest.raises(PeerStoreError, match="PEER_CONFIG_INVALID"):
        decode_settings(duplicate)


def test_readback_failure_restores_previous_config(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    store = PeerConfigStore(tmp_path)
    initial = store.load(create=True)
    original = store.path.read_bytes()
    original_load = store._load_unlocked
    calls = 0

    def corrupt_readback():
        nonlocal calls
        calls += 1
        return original_load() if calls == 1 else None

    monkeypatch.setattr(store, "_load_unlocked", corrupt_readback)
    record = PeerRecord(
        "peer-remote",
        "Remote",
        PROJECT,
        "http://127.0.0.1:8766",
        new_secret(),
        PROJECT,
    )
    with pytest.raises(
        PeerStoreError, match="PEER_CONFIG_READBACK_FAILED"
    ):
        store.upsert(record, expected_revision=initial.revision)
    assert store.path.read_bytes() == original


def test_auth_signature_binds_body_path_nonce_and_time() -> None:
    secret = new_secret()
    body = b'{"query":"marker"}'
    headers = signed_headers(
        "peer-local",
        secret,
        "POST",
        "/v1/query",
        body,
        now=1000,
        nonce="A" * 22,
    )
    verified = verify_signature(
        headers,
        secret,
        "POST",
        "/v1/query",
        body,
        now=1000,
    )
    assert verified[:2] == ("peer-local", "A" * 22)
    with pytest.raises(PeerContractError, match="PEER_AUTH_INVALID"):
        verify_signature(
            headers,
            secret,
            "POST",
            "/v1/query",
            b"changed",
            now=1000,
        )
