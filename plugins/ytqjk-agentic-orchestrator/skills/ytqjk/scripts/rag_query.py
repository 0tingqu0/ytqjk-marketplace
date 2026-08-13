from __future__ import annotations

from pathlib import Path
from typing import Any

from rag_common import read_chunks


GLOBAL_CACHE = "global-cache"


def vector_enabled(
    mode: str, stats: dict[str, int], config: dict[str, Any]
) -> bool:
    if mode == "on":
        return True
    if mode == "off":
        return False
    thresholds = config["auto"]
    return (
        stats["text_bytes"] >= int(thresholds["text_bytes"])
        or stats["chunks"] >= int(thresholds["chunks"])
    )


def build_vector_cache(
    project_dir: Path,
    knowledge_root: Path,
    config: dict[str, Any],
) -> dict[str, Any]:
    try:
        from vector_store import build_vectors

        embedding = config["embedding"]
        count = build_vectors(
            project_dir / "vectors",
            read_chunks(project_dir / "lexical.sqlite3"),
            str(embedding["model"]),
            int(embedding["dimensions"]),
            knowledge_root / "models",
        )
        return {"enabled": True, "status": "READY", "chunks": count, "error": None}
    except Exception as exc:
        return {
            "enabled": False,
            "status": "DEGRADED",
            "error": f"{type(exc).__name__}: {exc}",
        }


def query_vector_cache(
    cache_dir: Path,
    query: str,
    config: dict[str, Any],
    knowledge_root: Path,
    limit: int,
) -> list[dict[str, Any]]:
    from vector_store import query_vectors

    embedding = config["embedding"]
    return query_vectors(
        cache_dir / "vectors",
        query,
        str(embedding["model"]),
        knowledge_root / "models",
        max(limit * 3, 20),
    )
