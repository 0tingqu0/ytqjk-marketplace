from __future__ import annotations

from pathlib import Path

from file_lock import exclusive_file_lock
from path_safety import is_direct_directory, is_reparse
from project_source import project_identity
from rag_common import SCHEMA_VERSION, atomic_json, load_json, utc_now
from rag_locks import maintenance_lock, project_id_lock


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
    project_id = identified["id"]
    name = identified["name"]
    remote = identified["remote"]
    project_dir = knowledge_root / "projects" / project_id
    projects_dir = knowledge_root / "projects"
    catalog_path = knowledge_root / "catalog.json"
    with exclusive_file_lock(project_id_lock(knowledge_root, project_id)):
        with exclusive_file_lock(maintenance_lock(knowledge_root)):
            _require_source(root)
            projects_dir.mkdir(parents=True, exist_ok=True)
            if is_reparse(projects_dir):
                raise ValueError("UNSAFE_PROJECT_DIRECTORY")
            if is_reparse(project_dir):
                raise ValueError("UNSAFE_PROJECT_DIRECTORY")
            project_dir.mkdir(exist_ok=True)
            if not is_direct_directory(project_dir, projects_dir):
                raise ValueError("UNSAFE_PROJECT_DIRECTORY")
            for relative in ("cache", "handoffs", "errors", "vectors"):
                target = project_dir / relative
                if is_reparse(target):
                    raise ValueError("UNSAFE_PROJECT_DIRECTORY")
                target.mkdir(exist_ok=True)
                if not is_direct_directory(target, project_dir):
                    raise ValueError("UNSAFE_PROJECT_DIRECTORY")
            with exclusive_file_lock(catalog_path.with_suffix(".lock")):
                catalog = load_json(
                    catalog_path,
                    {"schema_version": SCHEMA_VERSION, "projects": {}},
                )
                catalog["schema_version"] = SCHEMA_VERSION
                existing = catalog["projects"].get(project_id, {})
                aliases = sorted(
                    set(existing.get("path_aliases", []) + [str(root)])
                )
                catalog["projects"][project_id] = {
                    "name": name,
                    "remote": remote,
                    "path_aliases": aliases,
                    "last_accessed": utc_now(),
                    "tracking_state": "REGISTERED",
                }
                _require_source(root)
                atomic_json(catalog_path, catalog)
    return {"id": project_id, "name": name, "root": str(root)}


def require_tracked_project(knowledge_root: Path, project_id: str) -> None:
    catalog_path = knowledge_root / "catalog.json"
    if not catalog_path.is_file() or is_reparse(catalog_path):
        raise ValueError("PROJECT_REMOVED")
    with exclusive_file_lock(catalog_path.with_suffix(".lock")):
        catalog = load_json(catalog_path, {})
        projects = (
            catalog.get("projects") if isinstance(catalog, dict) else None
        )
        project_dir = knowledge_root / "projects" / project_id
        if (
            catalog.get("schema_version") != SCHEMA_VERSION
            or not isinstance(projects, dict)
            or not isinstance(projects.get(project_id), dict)
            or not is_direct_directory(
                project_dir,
                knowledge_root / "projects",
            )
        ):
            raise ValueError("PROJECT_REMOVED")


def _require_source(root: Path) -> None:
    if not root.is_dir():
        raise FileNotFoundError("PROJECT_SOURCE_MISSING")
