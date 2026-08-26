"""Authenticated no-proxy client for LAN knowledge peers."""

from __future__ import annotations

import json
import math
import urllib.error
import urllib.request
from pathlib import Path

from knowledge_peer_contract import (
    MAX_BODY_BYTES,
    PeerContractError,
    PeerRecord,
    identifier,
    signed_headers,
)
from knowledge_peer_response import verify_response_signature
from knowledge_peer_store import PeerConfigStore, PeerStoreError


MAX_RESPONSE_BYTES = 1024 * 1024


class PeerClientError(RuntimeError):
    """Remote peer is unavailable or violated the protocol."""


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args: object, **kwargs: object):
        del args, kwargs
        return None


class KnowledgePeerClient:
    def __init__(self, root: Path, timeout: float = 5.0) -> None:
        self.store = PeerConfigStore(root)
        self.timeout = timeout
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            _NoRedirect(),
        )

    def query(
        self,
        mount_id: str,
        project_id: str,
        query: str,
        limit: int,
    ) -> dict[str, object]:
        settings, peer = self._peer(mount_id, project_id)
        result = self._post(
            settings.local_peer_id,
            peer,
            "/v1/query",
            {
                "project_id": project_id,
                "node_id": peer.remote_node_id,
                "query": query,
                "limit": limit,
            },
        )
        _exact(result, {
            "ok", "status", "project_id", "node_id", "generation",
            "results", "peer_id",
        })
        if result.get("project_id") != project_id:
            raise PeerClientError("PEER_PROJECT_MISMATCH")
        if result.get("node_id") != peer.remote_node_id:
            raise PeerClientError("PEER_NODE_MISMATCH")
        rows = result.get("results")
        generation = result.get("generation")
        if (
            result.get("status") not in {"PEER_HIT", "PEER_MISS"}
            or result.get("peer_id") != peer.peer_id
            or type(generation) is not str
            or len(generation) > 4096
            or type(rows) is not list
            or len(rows) > limit
        ):
            raise PeerClientError("PEER_RESPONSE_INVALID")
        for row in rows:
            _query_row(row)
        result["peer_id"] = peer.peer_id
        result["mount_id"] = mount_id
        return result

    def material(
        self,
        mount_id: str,
        project_id: str,
        material_id: str,
        remote_library_node: str | None = None,
    ) -> dict[str, object]:
        settings, peer = self._peer(mount_id, project_id)
        library_node = remote_library_node or peer.remote_node_id
        try:
            identifier("library_node", library_node)
        except PeerContractError as error:
            raise PeerClientError(str(error)) from error
        result = self._post(
            settings.local_peer_id,
            peer,
            "/v1/material",
            {
                "project_id": project_id,
                "node_id": peer.remote_node_id,
                "library_node": library_node,
                "material_id": material_id,
            },
        )
        _exact(result, {
            "ok", "status", "peer_id", "project_id", "node_id",
            "library_node", "material",
        })
        if (
            result.get("status") != "MATERIAL_READY"
            or result.get("peer_id") != peer.peer_id
            or result.get("project_id") != project_id
            or result.get("node_id") != peer.remote_node_id
            or result.get("library_node") != library_node
        ):
            raise PeerClientError("PEER_RESPONSE_INVALID")
        material = result.get("material")
        _query_row(material)
        return material

    def health(
        self,
        mount_id: str,
        project_id: str,
    ) -> dict[str, object]:
        settings, peer = self._peer(mount_id, project_id)
        result = self._post(
            settings.local_peer_id,
            peer,
            "/v1/health",
            {"project_id": project_id},
        )
        _exact(result, {
            "ok", "status", "peer_id", "project_id",
            "export_node_id", "library_count", "capabilities",
        })
        export_node_id = result.get("export_node_id")
        library_count = result.get("library_count")
        try:
            identifier("export_node_id", export_node_id)
        except PeerContractError as error:
            raise PeerClientError("PEER_RESPONSE_INVALID") from error
        if (
            result.get("status") != "READY"
            or result.get("peer_id") != peer.peer_id
            or result.get("project_id") != project_id
            or export_node_id != peer.remote_node_id
            or type(library_count) is not int
            or library_count < 0
            or result.get("capabilities")
            != ["query-v1", "material-v1", "response-hmac-v1"]
        ):
            raise PeerClientError("PEER_RESPONSE_INVALID")
        return result

    def _peer(self, mount_id: str, project_id: str):
        try:
            settings = self.store.load()
        except PeerStoreError as error:
            raise PeerClientError(str(error)) from error
        peer = settings.peer(mount_id)
        if peer is None or not peer.enabled:
            raise PeerClientError("PEER_NOT_CONFIGURED")
        if peer.project_id != project_id:
            raise PeerClientError("PEER_PROJECT_MISMATCH")
        return settings, peer

    def _post(
        self,
        local_peer_id: str,
        peer: PeerRecord,
        path: str,
        value: dict[str, object],
    ) -> dict[str, object]:
        body = json.dumps(
            value,
            ensure_ascii=False,
            allow_nan=False,
            separators=(",", ":"),
            sort_keys=True,
        ).encode("utf-8")
        if len(body) > MAX_BODY_BYTES:
            raise PeerClientError("PEER_REQUEST_TOO_LARGE")
        try:
            headers = signed_headers(
                local_peer_id, peer.secret, "POST", path, body
            )
        except PeerContractError as error:
            raise PeerClientError(str(error)) from error
        headers["Content-Type"] = "application/json"
        nonce = headers["X-YTQJK-Nonce"]
        request = urllib.request.Request(
            peer.endpoint + path,
            data=body,
            headers=headers,
            method="POST",
        )
        try:
            with self.opener.open(request, timeout=self.timeout) as response:
                content = response.read(MAX_RESPONSE_BYTES + 1)
                status = response.status
                response_headers = response.headers
        except (
            OSError,
            urllib.error.HTTPError,
            urllib.error.URLError,
        ) as error:
            raise PeerClientError("PEER_UNAVAILABLE") from error
        if len(content) > MAX_RESPONSE_BYTES:
            raise PeerClientError("PEER_RESPONSE_INVALID")
        try:
            verify_response_signature(
                response_headers,
                peer.secret,
                peer.peer_id,
                status,
                path,
                nonce,
                content,
            )
        except PeerContractError as error:
            raise PeerClientError("PEER_RESPONSE_INVALID") from error
        if status != 200:
            raise PeerClientError("PEER_RESPONSE_INVALID")
        try:
            result = json.loads(
                content.decode("utf-8"),
                object_pairs_hook=_unique_object,
            )
        except (UnicodeError, json.JSONDecodeError, ValueError) as error:
            raise PeerClientError("PEER_RESPONSE_INVALID") from error
        if type(result) is not dict or result.get("ok") is not True:
            raise PeerClientError("PEER_RESPONSE_INVALID")
        return result


def _query_row(value: object) -> None:
    fields = {
        "material_id", "library_node", "path", "line_start", "line_end",
        "content", "source_sha256", "scope", "score",
    }
    if type(value) is not dict or set(value) != fields:
        raise PeerClientError("PEER_RESPONSE_INVALID")
    material_id = value["material_id"]
    source_hash = value["source_sha256"]
    if (
        type(material_id) is not str
        or material_id.split(":", 1)[0]
        not in {"project", "prefetch", "library"}
        or len(material_id.split(":", 1)) != 2
        or len(material_id.split(":", 1)[1]) != 64
        or set(material_id.split(":", 1)[1])
        - set("0123456789abcdef")
        or type(source_hash) is not str
        or len(source_hash) != 64
        or set(source_hash) - set("0123456789abcdef")
    ):
        raise PeerClientError("PEER_RESPONSE_INVALID")
    try:
        identifier("library_node", value["library_node"])
    except PeerContractError as error:
        raise PeerClientError("PEER_RESPONSE_INVALID") from error
    text_fields = ("path", "content", "scope")
    if any(type(value[name]) is not str for name in text_fields):
        raise PeerClientError("PEER_RESPONSE_INVALID")
    if len(value["path"]) > 4096 or len(value["content"]) > 24_000:
        raise PeerClientError("PEER_RESPONSE_INVALID")
    if not 0 < len(value["scope"]) <= 128:
        raise PeerClientError("PEER_RESPONSE_INVALID")
    line_start = value["line_start"]
    line_end = value["line_end"]
    if (
        type(line_start) is not int
        or type(line_end) is not int
        or not 1 <= line_start <= line_end
    ):
        raise PeerClientError("PEER_RESPONSE_INVALID")
    score = value["score"]
    if (
        type(score) not in {int, float}
        or not math.isfinite(float(score))
    ):
        raise PeerClientError("PEER_RESPONSE_INVALID")


def _exact(value: dict[str, object], fields: set[str]) -> None:
    if set(value) != fields:
        raise PeerClientError("PEER_RESPONSE_INVALID")


def _unique_object(
    pairs: list[tuple[str, object]],
) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("DUPLICATE_RESPONSE_FIELD")
        value[key] = item
    return value


__all__ = ["KnowledgePeerClient", "PeerClientError"]
