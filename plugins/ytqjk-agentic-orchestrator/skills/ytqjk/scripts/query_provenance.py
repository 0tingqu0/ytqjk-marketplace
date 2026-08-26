"""Validate governed query rows before return and project caching."""

from __future__ import annotations

from pathlib import Path
from typing import Any

from file_lock import exclusive_file_lock
from global_store import retain_current_approved_hits
from project_prefetch import update_prefetch
from project_source import project_query_state
from rag_common import config_fingerprint
from rag_locks import project_id_lock


def current_governed_results(
    knowledge_root: Path,
    rows: list[dict[str, Any]],
    ancestor: bool,
) -> list[dict[str, Any]]:
    governed = ancestor or any(
        row.get("scope") == "project-prefetch-cache"
        for row in rows
    )
    if not governed:
        return rows
    return retain_current_approved_hits(knowledge_root, rows)


def cache_ancestor_results(
    knowledge_root: Path,
    project_dir: Path,
    project_id: str,
    query: str,
    rows: list[dict[str, Any]],
    generation: str,
) -> tuple[list[dict[str, Any]], list[dict[str, object]]]:
    current = retain_current_approved_hits(knowledge_root, rows)
    if not current:
        return [], []
    with exclusive_file_lock(
        project_id_lock(knowledge_root, project_id)
    ):
        cached = update_prefetch(
            project_dir,
            query,
            current,
            generation=generation,
            knowledge_root=knowledge_root,
        )
    return (
        retain_current_approved_hits(knowledge_root, current),
        cached,
    )


def project_stale(
    project_root: Path,
    manifest: dict[str, object],
    config: dict[str, Any],
) -> bool:
    indexed = manifest.get("indexed_identity")
    if not isinstance(indexed, dict):
        indexed = manifest.get("identity")
    if not isinstance(indexed, dict):
        return True
    current = project_query_state(project_root)
    if current["head"] == "NON_GIT":
        return True
    return (
        current["head"] != indexed.get("head")
        or current["dirty"] != "false"
        or indexed.get("dirty") not in {"false", "not-applicable"}
        or current.get("materialization")
        != indexed.get("materialization")
        or manifest.get("config_fingerprint")
        != config_fingerprint(config)
    )


__all__ = [
    "cache_ancestor_results",
    "current_governed_results",
    "project_stale",
]
