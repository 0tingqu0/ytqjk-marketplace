"""Authenticated read-only LAN server for project knowledge chunks."""

from __future__ import annotations

import json
import threading
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from knowledge_peer_contract import (
    MAX_BODY_BYTES,
    PeerContractError,
    PeerRecord,
    identifier,
    verify_signature,
)
from knowledge_peer_query import (
    PeerQueryError,
    fetch_subtree_material,
    query_library_subtree,
)
from knowledge_peer_replay import PeerReplayError, ReplayGuard
from knowledge_peer_response import signed_response_headers
from knowledge_peer_scope import PeerScopeError, export_catalog
from knowledge_peer_store import PeerConfigStore, PeerStoreError


class KnowledgePeerHandler(BaseHTTPRequestHandler):
    knowledge_root: Path
    store: PeerConfigStore
    replay: ReplayGuard

    def do_POST(self) -> None:  # noqa: N802
        if self.path not in {"/v1/health", "/v1/query", "/v1/material"}:
            self._send_error(HTTPStatus.NOT_FOUND, "PEER_API_NOT_FOUND")
            return
        try:
            body = self._body()
            settings = self.store.load()
            raw_peer = self.headers.get("X-YTQJK-Peer", "")
            peer_id = identifier("peer_id", raw_peer)
            peer = settings.peer(peer_id)
            if peer is None or not peer.enabled:
                raise PeerContractError("PEER_NOT_AUTHORIZED")
            verified, nonce, timestamp = verify_signature(
                self.headers,
                peer.secret,
                "POST",
                self.path,
                body,
            )
            if verified != peer.peer_id:
                raise PeerContractError("PEER_AUTH_INVALID")
            if not self.replay.accept(verified, nonce, timestamp):
                raise PeerContractError("PEER_REPLAY_REJECTED")
            payload = _decode(body)
            project_id = _project(payload, peer.project_id)
            result = self._route(
                settings.local_peer_id, peer, project_id, payload
            )
            self._send(
                {"ok": True, **result},
                response_peer_id=settings.local_peer_id,
                response_secret=peer.secret,
                request_nonce=nonce,
            )
        except PeerContractError as error:
            self._send_error(HTTPStatus.UNAUTHORIZED, str(error))
        except PeerReplayError as error:
            self._send_error(HTTPStatus.SERVICE_UNAVAILABLE, str(error))
        except PeerQueryError as error:
            self._send_error(HTTPStatus.BAD_REQUEST, str(error))
        except PeerScopeError as error:
            self._send_error(HTTPStatus.BAD_REQUEST, str(error))
        except PeerStoreError as error:
            self._send_error(HTTPStatus.SERVICE_UNAVAILABLE, str(error))
        except (UnicodeError, json.JSONDecodeError, ValueError) as error:
            self._send_error(
                HTTPStatus.BAD_REQUEST,
                str(error) or "PEER_REQUEST_INVALID",
            )

    def _route(
        self,
        local_peer_id: str,
        peer: PeerRecord,
        project_id: str,
        payload: dict[str, object],
    ) -> dict[str, object]:
        if self.path == "/v1/health":
            _exact(payload, {"project_id"})
            exports, library_count = export_catalog(
                self.knowledge_root,
                project_id,
                peer.export_node_ids or (),
            )
            return {
                "status": "READY",
                "peer_id": local_peer_id,
                "project_id": project_id,
                "export_nodes": [item.public() for item in exports],
                "library_count": library_count,
                "capabilities": [
                    "query-v1",
                    "material-v1",
                    "response-hmac-v1",
                ],
            }
        if self.path == "/v1/query":
            _exact(payload, {"project_id", "node_id", "query", "limit"})
            node_id = payload["node_id"]
            if node_id not in (peer.export_node_ids or ()):
                raise PeerQueryError("PEER_EXPORT_NODE_FORBIDDEN")
            result = query_library_subtree(
                self.knowledge_root,
                project_id,
                node_id,
                payload["query"],
                payload["limit"],
            )
            result["peer_id"] = local_peer_id
            return result
        _exact(payload, {
            "project_id", "node_id", "library_node", "material_id",
        })
        node_id = payload["node_id"]
        if node_id not in (peer.export_node_ids or ()):
            raise PeerQueryError("PEER_EXPORT_NODE_FORBIDDEN")
        material_id = payload["material_id"]
        library_node = payload["library_node"]
        if type(material_id) is not str or type(library_node) is not str:
            raise PeerQueryError("INVALID_MATERIAL_ID")
        return {
            "status": "MATERIAL_READY",
            "peer_id": local_peer_id,
            "project_id": project_id,
            "node_id": node_id,
            "library_node": library_node,
            "material": fetch_subtree_material(
                self.knowledge_root,
                project_id,
                node_id,
                library_node,
                material_id,
            ),
        }

    def _body(self) -> bytes:
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError as error:
            raise PeerContractError("PEER_LENGTH_INVALID") from error
        content_type = self.headers.get("Content-Type", "")
        if (
            not 0 < length <= MAX_BODY_BYTES
            or content_type.split(";", 1)[0].strip()
            != "application/json"
        ):
            raise PeerContractError("PEER_REQUEST_INVALID")
        return self.rfile.read(length)

    def _send_error(self, status: int, code: str) -> None:
        self._send({"ok": False, "error": code}, status)

    def _send(
        self,
        value: dict[str, object],
        status: int = HTTPStatus.OK,
        response_peer_id: str | None = None,
        response_secret: str | None = None,
        request_nonce: str | None = None,
    ) -> None:
        body = json.dumps(
            value, ensure_ascii=False, allow_nan=False
        ).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(body)))
        if (
            response_peer_id is not None
            and response_secret is not None
            and request_nonce is not None
        ):
            headers = signed_response_headers(
                response_peer_id,
                response_secret,
                int(status),
                self.path,
                request_nonce,
                body,
            )
            for name, value in headers.items():
                self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format: str, *_args: object) -> None:
        return


def start_peer_server(
    root: Path,
) -> tuple[ThreadingHTTPServer, threading.Thread] | None:
    store = PeerConfigStore(root)
    try:
        settings = store.load()
    except PeerStoreError as error:
        if str(error) == "PEER_CONFIG_NOT_CONFIGURED":
            return None
        raise
    if not settings.enabled:
        return None
    attributes = {
        "knowledge_root": Path(root).resolve(),
        "store": store,
        "replay": ReplayGuard(root),
    }
    handler = type("RootPeerHandler", (KnowledgePeerHandler,), attributes)
    server = ThreadingHTTPServer(
        (settings.bind_host, settings.port), handler
    )
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, thread


def _decode(content: bytes) -> dict[str, object]:
    def unique(pairs: list[tuple[str, object]]) -> dict[str, object]:
        value: dict[str, object] = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("DUPLICATE_REQUEST_FIELD")
            value[key] = item
        return value

    value = json.loads(
        content.decode("utf-8"), object_pairs_hook=unique
    )
    if type(value) is not dict:
        raise ValueError("PEER_REQUEST_INVALID")
    return value


def _project(
    payload: dict[str, object],
    authorized: str,
) -> str:
    project_id = payload.get("project_id")
    if type(project_id) is not str or project_id != authorized:
        raise PeerContractError("PEER_PROJECT_FORBIDDEN")
    return project_id


def _exact(value: dict[str, object], fields: set[str]) -> None:
    if set(value) != fields:
        raise ValueError("INVALID_PEER_REQUEST_FIELDS")


__all__ = [
    "KnowledgePeerHandler",
    "ReplayGuard",
    "start_peer_server",
]
