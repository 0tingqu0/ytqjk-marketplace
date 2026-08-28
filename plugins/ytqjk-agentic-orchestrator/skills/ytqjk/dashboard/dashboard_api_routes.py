"""Small route dispatcher shared by the dashboard HTTP handler."""

from __future__ import annotations

from dashboard_knowledge_graph_http import (
    handle_knowledge_graph_get,
    handle_knowledge_graph_post,
)
from dashboard_tree_http import handle_tree_get, handle_tree_post


def handle_dashboard_get(handler: object, path: str, query: str) -> bool:
    return (
        handle_knowledge_graph_get(handler, path, query)
        or handle_tree_get(handler, path)
    )


def handle_dashboard_post(handler: object, path: str) -> bool:
    return (
        handle_knowledge_graph_post(handler, path)
        or handle_tree_post(handler, path)
    )


__all__ = ["handle_dashboard_get", "handle_dashboard_post"]
