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


def test_upsert_preserves_directional_nodes_and_redacts_secret(
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
        "remote_node_id": "remote-library",
        "export_node_id": "local-library",
        "allow_insecure": False,
        "enabled": True,
    })
    public = result["peer_service"]
    record = public["peers"][0]
    assert record["remote_node_id"] == "remote-library"
    assert record["export_node_id"] == "local-library"
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
        "export_node_id": "local-library",
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
