"""Strict validation helpers for authenticated peer responses."""

from __future__ import annotations

import math

from knowledge_peer_contract import PeerContractError, identifier, text


class PeerClientError(RuntimeError):
    """Remote peer is unavailable or violated the protocol."""


def exact_response(value: dict[str, object], fields: set[str]) -> None:
    if set(value) != fields:
        raise PeerClientError("PEER_RESPONSE_INVALID")


def unique_object(
    pairs: list[tuple[str, object]],
) -> dict[str, object]:
    value: dict[str, object] = {}
    for key, item in pairs:
        if key in value:
            raise ValueError("DUPLICATE_RESPONSE_FIELD")
        value[key] = item
    return value


def validate_query_row(value: object) -> None:
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


def validate_export_nodes(value: object) -> None:
    if type(value) is not list or not 1 <= len(value) <= 64:
        raise PeerClientError("PEER_RESPONSE_INVALID")
    seen: set[str] = set()
    for item in value:
        if type(item) is not dict or set(item) != {"id", "title", "type"}:
            raise PeerClientError("PEER_RESPONSE_INVALID")
        try:
            node_id = identifier("export_node_id", item["id"])
            text("export_title", item["title"], 200)
        except PeerContractError as error:
            raise PeerClientError("PEER_RESPONSE_INVALID") from error
        if item["type"] not in {"global", "group", "project"}:
            raise PeerClientError("PEER_RESPONSE_INVALID")
        if node_id in seen:
            raise PeerClientError("PEER_RESPONSE_INVALID")
        seen.add(node_id)


__all__ = [
    "PeerClientError",
    "exact_response",
    "unique_object",
    "validate_export_nodes",
    "validate_query_row",
]
