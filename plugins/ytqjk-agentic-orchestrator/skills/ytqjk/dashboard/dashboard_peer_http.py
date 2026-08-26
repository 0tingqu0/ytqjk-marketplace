"""Local dashboard routes for peer configuration and explicit dispatch."""

from __future__ import annotations

import json
from http import HTTPStatus

from dashboard_peer_api import DashboardPeerApi, DashboardPeerApiError


_POST = {
    "/api/peers/bootstrap": "bootstrap",
    "/api/peers/configure": "configure",
    "/api/peers/discover": "discover",
    "/api/peers/dispatch": "dispatch",
    "/api/peers/health": "health",
    "/api/peers/material": "material",
    "/api/peers/remove": "remove",
    "/api/peers/secret": "secret",
    "/api/peers/upsert": "upsert",
}


def handle_peer_get(handler: object, path: str) -> bool:
    if path != "/api/peers":
        return False
    try:
        result = DashboardPeerApi(handler.knowledge_root).snapshot()
        result["runtime"] = getattr(
            handler, "peer_runtime_status", {"status": "UNKNOWN"}
        )
        handler.send_json(result)
    except DashboardPeerApiError as error:
        handler.send_json(error.payload(), error.status)
    return True


def handle_peer_post(handler: object, path: str) -> bool:
    action = _POST.get(path)
    if action is None:
        return False
    try:
        payload = handler.read_payload()
        service = DashboardPeerApi(handler.knowledge_root)
        result = getattr(service, action)(payload)
        handler.send_json(result)
    except DashboardPeerApiError as error:
        handler.send_json(error.payload(), error.status)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
        body = DashboardPeerApiError(
            HTTPStatus.BAD_REQUEST,
            str(error) or "INVALID_PEER_REQUEST",
        ).payload()
        handler.send_json(body, HTTPStatus.BAD_REQUEST)
    return True


__all__ = ["handle_peer_get", "handle_peer_post"]
