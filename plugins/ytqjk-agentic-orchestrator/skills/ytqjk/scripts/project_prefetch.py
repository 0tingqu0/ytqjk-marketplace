from __future__ import annotations

from pathlib import Path
from typing import Any

from rag_common import atomic_json, load_json, utc_now


MAX_PREFETCH_ENTRIES = 20


def update_prefetch(
    project_dir: Path, query: str, rows: list[dict[str, Any]]
) -> list[dict[str, object]]:
    fresh = [
        {
            "path": str(row["path"]),
            "line_start": int(row["line_start"]),
            "line_end": int(row["line_end"]),
            "content": str(row["content"]),
            "query": query,
            "cached_at": utc_now(),
        }
        for row in rows
        if all(key in row for key in ("path", "line_start", "line_end", "content"))
    ]
    cache_path = project_dir / "cache" / "global-knowledge.json"
    existing = load_json(cache_path, {}).get("entries", [])
    prior = existing if isinstance(existing, list) else []
    entries, seen = [], set()
    for entry in fresh + prior:
        if not isinstance(entry, dict):
            continue
        key = (entry.get("path"), entry.get("line_start"), entry.get("line_end"))
        if key in seen:
            continue
        seen.add(key); entries.append(entry)
        if len(entries) == MAX_PREFETCH_ENTRIES:
            break
    atomic_json(cache_path, {"entries": entries})
    return entries
