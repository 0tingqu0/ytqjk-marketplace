from __future__ import annotations

import hashlib
import os
import re
from pathlib import Path, PurePosixPath

from rag_common import SCHEMA_VERSION, atomic_json, load_json, run_git, utc_now
from rag_security import normalize_remote


def identify_project(project: Path) -> dict[str, str]:
    root = Path(run_git(project, "rev-parse", "--show-toplevel").strip()).resolve()
    common = Path(run_git(root, "rev-parse", "--git-common-dir").strip())
    common = (common if common.is_absolute() else root / common).resolve()
    canonical_root = common.parent if os.path.normcase(common.name) == ".git" else common
    remote = normalize_remote(run_git(root, "remote", "get-url", "origin", check=False).strip())
    identity = remote or os.path.normcase(str(canonical_root))
    name_source = PurePosixPath(remote).name if remote else canonical_root.name
    name = re.sub(r"[^a-zA-Z0-9._-]+", "-", name_source).strip("-_") or "project"
    project_id = f"{name}--{hashlib.sha256(identity.encode('utf-8')).hexdigest()[:12]}"
    return {"id": project_id, "name": name, "root": str(root), "remote": remote}


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
    catalog = load_json(catalog_path, {"schema_version": SCHEMA_VERSION, "projects": {}})
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
