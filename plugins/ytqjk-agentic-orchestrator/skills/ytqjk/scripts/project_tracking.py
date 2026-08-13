from __future__ import annotations

from pathlib import Path

from file_lock import exclusive_file_lock
from project_source import project_identity
from rag_common import SCHEMA_VERSION, atomic_json, load_json, utc_now


def identify_project(project: Path) -> dict[str, str]:
    identity = project_identity(project)
    return {
        "id": identity["id"],
        "name": identity["name"],
        "root": identity["root"],
        "remote": identity["remote"],
    }


def track_project(
    knowledge_root: Path,
    project: Path,
    identity: dict[str, str] | None = None,
) -> dict[str, str]:
    identified = identity or identify_project(project)
    root = Path(identified["root"])
    project_id, name, remote = identified["id"], identified["name"], identified["remote"]
    project_dir = knowledge_root / "projects" / project_id
    for relative in ("cache", "handoffs", "errors", "vectors"):
        (project_dir / relative).mkdir(parents=True, exist_ok=True)
    catalog_path = knowledge_root / "catalog.json"
    with exclusive_file_lock(catalog_path.with_suffix(".lock")):
        catalog = load_json(
            catalog_path, {"schema_version": SCHEMA_VERSION, "projects": {}}
        )
        catalog["schema_version"] = SCHEMA_VERSION
        existing = catalog["projects"].get(project_id, {})
        aliases = sorted(set(existing.get("path_aliases", []) + [str(root)]))
        catalog["projects"][project_id] = {
            "name": name,
            "remote": remote,
            "path_aliases": aliases,
            "last_accessed": utc_now(),
            "tracking_state": "REGISTERED",
        }
        atomic_json(catalog_path, catalog)
    return {"id": project_id, "name": name, "root": str(root)}
