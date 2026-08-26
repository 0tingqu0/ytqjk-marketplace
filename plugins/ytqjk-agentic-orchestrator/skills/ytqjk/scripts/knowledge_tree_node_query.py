"""Execute one local or mounted node in a knowledge query chain."""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from file_lock import exclusive_file_lock
from global_store import is_current_approved_hit, retain_current_approved_hits
from knowledge_peer_client import KnowledgePeerClient, PeerClientError
from project_prefetch import query_prefetch
from rag_common import SCHEMA_VERSION, config_fingerprint, load_json


GLOBAL_CONFIG_ERROR = "全局知识索引配置已变化，需要重新建立索引。"
GLOBAL_GENERATION_ERROR = "全局知识索引缺少代际信息，需要重新建立索引。"
GLOBAL_INDEX_ERROR = "全局知识索引不可用或已过期，需要重新建立索引。"
PROJECT_INDEX_ERROR = "项目知识索引安全版本已过期，需要重新建立索引。"


@dataclass(frozen=True)
class QueryNode:
    node_id: str
    kind: str
    cache_dir: Path | None
    lock_path: Path | None
    scope: str
    unavailable_reason: str | None = None
    mount_id: str | None = None


QueryIndex = Callable[..., list[dict[str, Any]]]
ProjectStale = Callable[[Path, dict[str, object], dict[str, Any]], bool]


def query_node(
    node: QueryNode,
    current_leaf: bool,
    knowledge_root: Path,
    project_root: Path,
    config: dict[str, Any],
    query: str,
    limit: int,
    *,
    query_index: QueryIndex,
    project_stale: ProjectStale,
    request_project_id: str | None = None,
) -> dict[str, object]:
    if node.unavailable_reason is not None:
        return {
            "results": [],
            "stale": current_leaf,
            "unavailable_reason": node.unavailable_reason,
        }
    if node.kind == "mounted":
        return _query_mounted_node(
            node,
            knowledge_root,
            request_project_id,
            query,
            limit,
        )
    if node.cache_dir is None or node.lock_path is None:
        raise RuntimeError("INVALID_QUERY_NODE_LOCATION")
    with exclusive_file_lock(node.lock_path):
        return _query_available_node(
            node,
            current_leaf,
            knowledge_root,
            project_root,
            config,
            query,
            limit,
            query_index,
            project_stale,
        )


def _query_available_node(
    node: QueryNode,
    current_leaf: bool,
    knowledge_root: Path,
    project_root: Path,
    config: dict[str, Any],
    query: str,
    limit: int,
    query_index: QueryIndex,
    project_stale: ProjectStale,
) -> dict[str, object]:
    if node.cache_dir is None:
        raise RuntimeError("INVALID_QUERY_NODE_LOCATION")
    manifest_path = node.cache_dir / "manifest.json"
    database = node.cache_dir / "lexical.sqlite3"
    manifest = load_json(manifest_path, {})
    absent = not manifest and not database.is_file()
    if absent and not current_leaf:
        return {
            "results": [],
            "stale": False,
            "unavailable_reason": "INDEX_NOT_CONFIGURED",
        }
    _validate_index(node, manifest, database, current_leaf, absent, config)
    stale = (
        project_stale(project_root, manifest, config)
        if current_leaf else False
    )
    validator = None
    if not current_leaf:
        validator = lambda row: is_current_approved_hit(
            knowledge_root, row
        )
    results = query_index(
        node.cache_dir,
        database,
        manifest_path,
        manifest,
        knowledge_root,
        config,
        query,
        limit,
        node.scope,
        allow_vector=not stale,
        project_cache=current_leaf,
        validator=validator,
        read_only=not current_leaf,
    )
    if not current_leaf:
        results = retain_current_approved_hits(knowledge_root, results)
    if current_leaf and not results:
        results = query_prefetch(
            node.cache_dir,
            query,
            limit,
            knowledge_root=knowledge_root,
        )
    indexed_at = manifest.get("indexed_at")
    if current_leaf and results and results[0].get("cached_at"):
        indexed_at = results[0].get("cached_at")
    return {
        "generation": _generation(node, manifest, current_leaf),
        "indexed_at": indexed_at,
        "results": results,
        "stale": stale,
    }


def _validate_index(
    node: QueryNode,
    manifest: dict[str, Any],
    database: Path,
    current_leaf: bool,
    absent: bool,
    config: dict[str, Any],
) -> None:
    if database.is_file() and manifest.get("schema_version") != SCHEMA_VERSION:
        if current_leaf:
            raise RuntimeError(PROJECT_INDEX_ERROR)
        if node.kind == "global":
            raise RuntimeError(GLOBAL_INDEX_ERROR)
        raise RuntimeError("KNOWLEDGE_NODE_INDEX_SCHEMA_INVALID")
    if not current_leaf and not absent and not database.is_file():
        if node.kind == "global":
            raise RuntimeError(GLOBAL_INDEX_ERROR)
        raise RuntimeError("KNOWLEDGE_NODE_INDEX_INCOMPLETE")
    configured = manifest.get("config_fingerprint")
    if (
        not current_leaf
        and configured is not None
        and configured != config_fingerprint(config)
    ):
        if node.kind == "global":
            raise RuntimeError(GLOBAL_CONFIG_ERROR)
        raise RuntimeError("KNOWLEDGE_NODE_INDEX_CONFIG_CHANGED")


def _generation(
    node: QueryNode,
    manifest: dict[str, Any],
    current_leaf: bool,
) -> str:
    if current_leaf:
        return ""
    generation = str(
        manifest.get("generation")
        or manifest.get("source_fingerprint")
        or manifest.get("indexed_at")
        or ""
    )
    if not generation:
        if node.kind == "global":
            raise RuntimeError(GLOBAL_GENERATION_ERROR)
        raise RuntimeError("KNOWLEDGE_NODE_GENERATION_MISSING")
    return f"tree:{node.node_id}:{generation}"


def _query_mounted_node(
    node: QueryNode,
    knowledge_root: Path,
    project_id: str | None,
    query: str,
    limit: int,
) -> dict[str, object]:
    if node.mount_id is None or not project_id:
        return {
            "results": [],
            "stale": False,
            "unavailable_reason": "PEER_PROJECT_NOT_CONFIGURED",
        }
    try:
        response = KnowledgePeerClient(knowledge_root).query(
            node.mount_id, project_id, query, limit
        )
    except PeerClientError as error:
        return {
            "results": [],
            "stale": False,
            "unavailable_reason": str(error),
        }
    results = response.get("results")
    if type(results) is not list:
        return {
            "results": [],
            "stale": False,
            "unavailable_reason": "PEER_RESPONSE_INVALID",
        }
    return {
        "generation": str(response.get("generation", "")),
        "indexed_at": None,
        "peer_id": response.get("peer_id"),
        "results": results,
        "stale": False,
    }


__all__ = ["QueryNode", "query_node"]
