from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest


ROOT = Path(__file__).resolve().parents[1]
DASHBOARD = (
    ROOT / "plugins" / "ytqjk-agentic-orchestrator" / "skills"
    / "ytqjk" / "dashboard"
)
SCRIPTS = DASHBOARD.parent / "scripts"
sys.path.insert(0, str(DASHBOARD))
sys.path.insert(0, str(SCRIPTS))

import dashboard_peer_api as peer_api  # noqa: E402
from knowledge_peer_contract import new_secret  # noqa: E402


def test_upsert_supports_inbound_only_and_multiple_open_libraries(
    tmp_path: Path,
) -> None:
    service = peer_api.DashboardPeerApi(tmp_path)
    initial = service.bootstrap({})["peer_service"]
    secret = new_secret()
    result = service.upsert({
        "expected_revision": initial["revision"],
        "peer_id": "peer-remote",
        "title": "Remote",
        "project_id": "project--0123456789ab",
        "endpoint": "http://127.0.0.1:8766",
        "secret": secret,
        "remote_node_id": None,
        "export_node_ids": ["local-library", "local-child"],
        "allow_insecure": False,
        "enabled": True,
    })
    public = result["peer_service"]
    record = public["peers"][0]
    assert record["remote_node_id"] is None
    assert record["export_node_id"] == "local-library"
    assert record["export_node_ids"] == ["local-library", "local-child"]
    assert "secret" not in json.dumps(public)
    assert secret not in json.dumps(public)
    edited = service.upsert({
        "expected_revision": public["revision"],
        "peer_id": "peer-remote",
        "title": "Remote edited",
        "project_id": "project--0123456789ab",
        "endpoint": "http://127.0.0.1:8766",
        "secret": None,
        "remote_node_id": "remote-library",
        "export_node_ids": ["local-library", "local-child"],
        "allow_insecure": False,
        "enabled": True,
    })["peer_service"]
    assert edited["peers"][0]["key_fingerprint"] == (
        record["key_fingerprint"]
    )

    with pytest.raises(
        peer_api.DashboardPeerApiError,
        match="INVALID_PEER_REQUEST_FIELDS",
    ):
        service.upsert({
            "expected_revision": edited["revision"],
            "peer_id": "peer-remote",
            "title": "Remote",
            "project_id": "project--0123456789ab",
            "endpoint": "http://127.0.0.1:8766",
            "secret": secret,
            "remote_node_id": "remote-library",
            "allow_insecure": False,
            "enabled": True,
        })


def test_upsert_accepts_legacy_single_export_field(tmp_path: Path) -> None:
    service = peer_api.DashboardPeerApi(tmp_path)
    initial = service.bootstrap({})["peer_service"]

    result = service.upsert({
        "expected_revision": initial["revision"],
        "peer_id": "peer-legacy",
        "title": "Legacy",
        "project_id": "project--0123456789ab",
        "endpoint": "http://127.0.0.1:8766",
        "secret": new_secret(),
        "remote_node_id": "remote-library",
        "export_node_id": "local-library",
        "allow_insecure": False,
        "enabled": True,
    })

    assert result["peer_service"]["peers"][0]["export_node_ids"] == [
        "local-library"
    ]


def test_discover_uses_saved_secret_without_persisting_remote_choice(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    service = peer_api.DashboardPeerApi(tmp_path)
    initial = service.bootstrap({})["peer_service"]
    secret = new_secret()
    saved = service.upsert({
        "expected_revision": initial["revision"],
        "peer_id": "peer-remote",
        "title": "Remote",
        "project_id": "project--0123456789ab",
        "endpoint": "http://127.0.0.1:8766",
        "secret": secret,
        "remote_node_id": None,
        "export_node_ids": ["local-library"],
        "allow_insecure": False,
        "enabled": True,
    })["peer_service"]
    captured: dict[str, object] = {}

    def discover(_client: object, draft: object) -> dict[str, object]:
        captured["draft"] = draft
        return {
            "ok": True,
            "status": "READY",
            "peer_id": "peer-remote",
            "project_id": "project--0123456789ab",
            "export_nodes": [
                {"id": "remote-library", "title": "远端库", "type": "group"}
            ],
            "library_count": 1,
            "capabilities": [
                "query-v1", "material-v1", "response-hmac-v1"
            ],
        }

    monkeypatch.setattr(peer_api.KnowledgePeerClient, "discover", discover)
    result = service.discover({
        "peer_id": "peer-remote",
        "project_id": "project--0123456789ab",
        "endpoint": "http://127.0.0.1:8766",
        "secret": None,
        "allow_insecure": False,
    })

    draft = captured["draft"]
    assert draft.secret == secret
    assert draft.remote_node_id is None
    assert result["peer"]["export_nodes"][0]["id"] == "remote-library"
    assert service.snapshot()["peer_service"] == saved


def test_material_forwards_remote_descendant_node(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    captured: dict[str, object] = {}

    def fetch(
        root: Path,
        project_id: str,
        node_id: str,
        material_id: str,
        *,
        remote_library_node: str,
    ) -> dict[str, object]:
        captured.update({
            "root": root,
            "project_id": project_id,
            "node_id": node_id,
            "material_id": material_id,
            "remote_library_node": remote_library_node,
        })
        return {"ok": True, "status": "PEER_MATERIAL_READY"}

    monkeypatch.setattr(peer_api, "fetch_sibling_material", fetch)
    result = peer_api.DashboardPeerApi(tmp_path).material({
        "project_id": "project--0123456789ab",
        "node_id": "mounted-peer",
        "remote_library_node": "remote-child",
        "material_id": "library:" + "a" * 64,
    })
    assert result["status"] == "PEER_MATERIAL_READY"
    assert captured["remote_library_node"] == "remote-child"
