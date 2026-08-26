"""Local-only administration and dispatch API for LAN peers."""

from __future__ import annotations

from pathlib import Path

from knowledge_peer_client import KnowledgePeerClient, PeerClientError
from knowledge_peer_contract import PeerContractError, PeerRecord, new_secret
from knowledge_peer_dispatch import (
    dispatch_siblings,
    fetch_sibling_material,
)
from knowledge_peer_store import PeerConfigStore, PeerStoreError


class DashboardPeerApiError(ValueError):
    def __init__(self, status: int, code: str) -> None:
        super().__init__(code)
        self.status = status
        self.code = code

    def payload(self) -> dict[str, object]:
        return {
            "ok": False,
            "error": {
                "status": self.status,
                "code": self.code,
                "message": self.code,
            },
        }


class DashboardPeerApi:
    def __init__(self, root: Path) -> None:
        self.root = Path(root).resolve()
        self.store = PeerConfigStore(self.root)

    def snapshot(self) -> dict[str, object]:
        try:
            settings = self.store.load()
        except PeerStoreError as error:
            if str(error) == "PEER_CONFIG_NOT_CONFIGURED":
                return {
                    "ok": True,
                    "status": "NOT_CONFIGURED",
                    "peer_service": None,
                }
            raise _error(error) from error
        return {
            "ok": True,
            "status": "CONFIGURED",
            "peer_service": settings.public(),
        }

    def bootstrap(self, payload: object) -> dict[str, object]:
        _exact(payload, set())
        try:
            settings = self.store.load(create=True)
        except PeerStoreError as error:
            raise _error(error) from error
        return {
            "ok": True,
            "status": "CONFIGURED",
            "peer_service": settings.public(),
        }

    def secret(self, payload: object) -> dict[str, object]:
        _exact(payload, set())
        try:
            settings = self.store.load(create=True)
        except PeerStoreError as error:
            raise _error(error) from error
        return {
            "ok": True,
            "local_peer_id": settings.local_peer_id,
            "secret": new_secret(),
            "one_time_display": True,
        }

    def configure(self, payload: object) -> dict[str, object]:
        value = _exact(payload, {
            "expected_revision",
            "enabled",
            "bind_host",
            "port",
            "allow_insecure_lan",
        })
        try:
            settings = self.store.configure_local(
                enabled=value["enabled"],
                bind_host=value["bind_host"],
                port=value["port"],
                allow_insecure_lan=value["allow_insecure_lan"],
                expected_revision=value["expected_revision"],
            )
        except PeerStoreError as error:
            raise _error(error) from error
        return {
            "ok": True,
            "status": "RESTART_REQUIRED",
            "peer_service": settings.public(),
        }

    def upsert(self, payload: object) -> dict[str, object]:
        value, export_node_ids = _upsert_payload(payload)
        try:
            secret = _peer_secret(
                self.store, value["peer_id"], value["secret"]
            )
            record = PeerRecord(
                peer_id=value["peer_id"],
                title=value["title"],
                project_id=value["project_id"],
                endpoint=value["endpoint"],
                secret=secret,
                remote_node_id=value["remote_node_id"],
                export_node_ids=export_node_ids,
                allow_insecure=value["allow_insecure"],
                enabled=value["enabled"],
            )
            settings = self.store.upsert(
                record,
                expected_revision=value["expected_revision"],
            )
        except (PeerContractError, PeerStoreError) as error:
            raise _error(error) from error
        return {
            "ok": True,
            "status": "PEER_SAVED",
            "peer_service": settings.public(),
        }

    def discover(self, payload: object) -> dict[str, object]:
        value = _exact(payload, {
            "peer_id", "project_id", "endpoint", "secret",
            "allow_insecure",
        })
        try:
            settings = self.store.load()
            if value["peer_id"] == settings.local_peer_id:
                raise PeerStoreError("SELF_PEER_FORBIDDEN")
            secret = _peer_secret(
                self.store, value["peer_id"], value["secret"]
            )
            draft = PeerRecord(
                peer_id=value["peer_id"],
                title="Peer discovery",
                project_id=value["project_id"],
                endpoint=value["endpoint"],
                secret=secret,
                remote_node_id=None,
                export_node_ids=(value["project_id"],),
                allow_insecure=value["allow_insecure"],
            )
            result = KnowledgePeerClient(self.root).discover(draft)
        except PeerClientError as error:
            raise DashboardPeerApiError(503, str(error)) from error
        except (PeerContractError, PeerStoreError) as error:
            raise _error(error) from error
        return {
            "ok": True,
            "status": "PEER_DISCOVERED",
            "peer": result,
        }

    def remove(self, payload: object) -> dict[str, object]:
        value = _exact(payload, {"expected_revision", "peer_id"})
        try:
            settings = self.store.remove(
                value["peer_id"],
                expected_revision=value["expected_revision"],
            )
        except PeerStoreError as error:
            raise _error(error) from error
        return {
            "ok": True,
            "status": "PEER_REMOVED",
            "peer_service": settings.public(),
        }

    def health(self, payload: object) -> dict[str, object]:
        value = _exact(payload, {"mount_id", "project_id"})
        try:
            result = KnowledgePeerClient(self.root).health(
                value["mount_id"], value["project_id"]
            )
        except PeerClientError as error:
            raise DashboardPeerApiError(503, str(error)) from error
        return {"ok": True, "peer": result}

    def dispatch(self, payload: object) -> dict[str, object]:
        value = _exact(payload, {"project_id", "query", "limit"})
        try:
            return dispatch_siblings(
                self.root,
                value["project_id"],
                value["query"],
                value["limit"],
            )
        except (OSError, RuntimeError, ValueError) as error:
            raise _error(error) from error

    def material(self, payload: object) -> dict[str, object]:
        value = _exact(payload, {
            "project_id",
            "node_id",
            "remote_library_node",
            "material_id",
        })
        try:
            return fetch_sibling_material(
                self.root,
                value["project_id"],
                value["node_id"],
                value["material_id"],
                remote_library_node=value["remote_library_node"],
            )
        except (OSError, RuntimeError, ValueError) as error:
            raise _error(error) from error


def _exact(value: object, fields: set[str]) -> dict[str, object]:
    if type(value) is not dict or set(value) != fields:
        raise DashboardPeerApiError(400, "INVALID_PEER_REQUEST_FIELDS")
    return value


def _upsert_payload(
    payload: object,
) -> tuple[dict[str, object], tuple[str, ...]]:
    common = {
        "expected_revision", "peer_id", "title", "project_id",
        "endpoint", "secret", "remote_node_id", "allow_insecure",
        "enabled",
    }
    if type(payload) is not dict:
        raise DashboardPeerApiError(400, "INVALID_PEER_REQUEST_FIELDS")
    if set(payload) == common | {"export_node_ids"}:
        raw_ids = payload["export_node_ids"]
        if type(raw_ids) is not list:
            raise DashboardPeerApiError(400, "INVALID_EXPORT_NODE_IDS")
        return payload, tuple(raw_ids)
    if set(payload) == common | {"export_node_id"}:
        return payload, (payload["export_node_id"],)
    raise DashboardPeerApiError(400, "INVALID_PEER_REQUEST_FIELDS")


def _peer_secret(
    store: PeerConfigStore,
    peer_id: object,
    supplied: object,
) -> object:
    if supplied is not None:
        return supplied
    current = store.load().peer(peer_id)
    if current is None:
        raise DashboardPeerApiError(400, "PEER_SECRET_REQUIRED")
    return current.secret


def _error(error: Exception) -> DashboardPeerApiError:
    code = str(error) or "PEER_OPERATION_FAILED"
    if "REVISION" in code:
        return DashboardPeerApiError(409, code)
    if code in {"PEER_NOT_CONFIGURED", "PEER_CONFIG_NOT_CONFIGURED"}:
        return DashboardPeerApiError(503, code)
    return DashboardPeerApiError(400, code)


__all__ = [
    "DashboardPeerApi",
    "DashboardPeerApiError",
]
