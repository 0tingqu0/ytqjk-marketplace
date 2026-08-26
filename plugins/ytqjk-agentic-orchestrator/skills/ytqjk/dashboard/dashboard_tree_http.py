"""HTTP routing adapter for preview-gated knowledge-tree operations."""

from __future__ import annotations

import json
from http import HTTPStatus

from dashboard_peer_http import handle_peer_get, handle_peer_post
from dashboard_tree_api import DashboardTreeApi, DashboardTreeApiError
from knowledge_tree import RevisionConflict
from knowledge_tree_store import KnowledgeTreeStore, TreeStoreError


_TREE_COMMITS = {
    "/api/libraries/attach": "attach",
    "/api/libraries/create": "create",
    "/api/libraries/detach": "detach",
    "/api/libraries/insert-between": "insert_between",
    "/api/libraries/move": "move",
    "/api/libraries/rebuild-index": "rebuild_index",
}


def tree_service(handler: object) -> DashboardTreeApi:
    current = type(handler).tree_api
    if current is not None:
        return current
    with handler.update_lock:
        current = type(handler).tree_api
        if current is None:
            store = KnowledgeTreeStore(handler.knowledge_root / "tree.json")
            if not store.path.exists():
                catalog = handler.knowledge_root / "catalog.json"
                source = catalog if catalog.exists() else {"projects": {}}
                try:
                    store.bootstrap(source)
                except (RevisionConflict, TreeStoreError) as error:
                    raise DashboardTreeApiError(
                        HTTPStatus.SERVICE_UNAVAILABLE,
                        str(error) or "TREE_BOOTSTRAP_FAILED",
                    ) from error
            current = DashboardTreeApi(store)
            type(handler).tree_api = current
    return current


def handle_tree_get(handler: object, path: str) -> bool:
    if handle_peer_get(handler, path):
        return True
    if path != "/api/libraries/tree":
        return False
    try:
        handler.send_json(tree_service(handler).snapshot())
    except DashboardTreeApiError as error:
        handler.send_json(error.payload(), error.status)
    return True


def handle_tree_post(handler: object, path: str) -> bool:
    if handle_peer_post(handler, path):
        return True
    if path != "/api/libraries/preview" and path not in _TREE_COMMITS:
        return False
    try:
        payload = handler.read_payload()
        service = tree_service(handler)
        if path == "/api/libraries/preview":
            if set(payload) != {"action", "payload"}:
                raise DashboardTreeApiError(
                    HTTPStatus.BAD_REQUEST, "INVALID_REQUEST_FIELDS",
                )
            result = service.preview(payload["action"], payload["payload"])
        else:
            result = service.commit(_TREE_COMMITS[path], payload)
        handler.send_json(result)
    except DashboardTreeApiError as error:
        handler.send_json(error.payload(), error.status)
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
        body = DashboardTreeApiError(
            HTTPStatus.BAD_REQUEST,
            str(error) or "INVALID_TREE_REQUEST",
        ).payload()
        handler.send_json(body, HTTPStatus.BAD_REQUEST)
    return True


__all__ = ["handle_tree_get", "handle_tree_post", "tree_service"]
