"""HTTP contract for local semantic knowledge graph operations."""

from __future__ import annotations

import json
from http import HTTPStatus
from urllib.parse import parse_qs

from knowledge_graph_service import (
    build_knowledge_graph,
    explore_path,
    recommend_knowledge,
    semantic_search,
)


GRAPH_PATH = "/api/knowledge-graph"
POST_PATHS = {
    "/api/knowledge-search",
    "/api/knowledge-recommendations",
    "/api/knowledge-path",
}


def _error(handler: object, code: str, status: HTTPStatus) -> None:
    handler.send_json(
        {"ok": False, "error": {"code": code, "message": _message(code)}},
        status,
    )


def _message(code: str) -> str:
    return {
        "EMPTY_QUERY": "请输入要检索的概念或问题。",
        "QUERY_TOO_LONG": "检索内容过长，请缩短后重试。",
        "INVALID_LIMIT": "结果数量必须在 1 到 20 之间。",
        "INVALID_NODE_ID": "知识节点标识无效。",
        "INVALID_MAX_DEPTH": "路径深度必须在 1 到 6 之间。",
        "INVALID_REQUEST_FIELDS": "请求字段无效。",
        "GRAPH_UNAVAILABLE": "知识图谱暂时不可用，请稍后重试。",
    }.get(code, code)


def _limit(value: object, default: int, maximum: int = 20) -> int:
    if value is None:
        return default
    if type(value) is not int or not 1 <= value <= maximum:
        raise ValueError("INVALID_LIMIT")
    return value


def _fields(payload: dict[str, object], allowed: set[str]) -> None:
    if not set(payload) <= allowed:
        raise ValueError("INVALID_REQUEST_FIELDS")


def handle_knowledge_graph_get(
    handler: object, path: str, query: str,
) -> bool:
    if path != GRAPH_PATH:
        return False
    try:
        raw_limit = parse_qs(query).get("limit", ["100"])[0]
        limit = int(raw_limit)
        if not 20 <= limit <= 160:
            raise ValueError("INVALID_LIMIT")
        handler.send_json({
            "ok": True,
            **build_knowledge_graph(handler.knowledge_root, limit),
        })
    except ValueError as exc:
        code = str(exc) if str(exc).startswith("INVALID_") else "INVALID_LIMIT"
        _error(handler, code, HTTPStatus.BAD_REQUEST)
    except (OSError, RuntimeError, json.JSONDecodeError):
        _error(handler, "GRAPH_UNAVAILABLE", HTTPStatus.SERVICE_UNAVAILABLE)
    return True


def handle_knowledge_graph_post(handler: object, path: str) -> bool:
    if path not in POST_PATHS:
        return False
    try:
        payload = handler.read_payload()
        if path == "/api/knowledge-search":
            result = _search(handler, payload)
        elif path == "/api/knowledge-recommendations":
            result = _recommend(handler, payload)
        else:
            result = _path(handler, payload)
        handler.send_json({"ok": True, **result})
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        code = str(exc)
        if code not in {
            "EMPTY_QUERY", "QUERY_TOO_LONG", "INVALID_LIMIT",
            "INVALID_NODE_ID", "INVALID_MAX_DEPTH", "INVALID_REQUEST_FIELDS",
        }:
            code = "INVALID_REQUEST_FIELDS"
        _error(handler, code, HTTPStatus.BAD_REQUEST)
    except (OSError, RuntimeError):
        _error(handler, "GRAPH_UNAVAILABLE", HTTPStatus.SERVICE_UNAVAILABLE)
    return True


def _search(handler: object, payload: dict[str, object]) -> dict[str, object]:
    _fields(payload, {"query", "limit"})
    query = payload.get("query")
    if not isinstance(query, str):
        raise ValueError("EMPTY_QUERY")
    return semantic_search(
        handler.knowledge_root, query, _limit(payload.get("limit"), 8),
    )


def _recommend(handler: object, payload: dict[str, object]) -> dict[str, object]:
    _fields(payload, {"node_id", "limit"})
    node_id = payload.get("node_id")
    if not isinstance(node_id, str) or not 1 <= len(node_id) <= 96:
        raise ValueError("INVALID_NODE_ID")
    return recommend_knowledge(
        handler.knowledge_root, node_id, _limit(payload.get("limit"), 6),
    )


def _path(handler: object, payload: dict[str, object]) -> dict[str, object]:
    _fields(payload, {"source", "target", "max_depth"})
    source, target = payload.get("source"), payload.get("target")
    if (
        not isinstance(source, str) or not isinstance(target, str)
        or not source or not target or len(source) > 96 or len(target) > 96
    ):
        raise ValueError("INVALID_NODE_ID")
    depth = payload.get("max_depth", 5)
    if type(depth) is not int or not 1 <= depth <= 6:
        raise ValueError("INVALID_MAX_DEPTH")
    return explore_path(handler.knowledge_root, source, target, depth)


__all__ = ["handle_knowledge_graph_get", "handle_knowledge_graph_post"]
