from __future__ import annotations

import json
import os
import re
from pathlib import Path
from typing import Any

from path_safety import is_reparse
from rag_common import SCHEMA_VERSION, load_json


PROJECT_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*\Z")


class CleanupRejected(ValueError):
    """Cleanup request failed a fail-closed eligibility check."""


def contained(path: Path, parent: Path) -> bool:
    try:
        path.resolve().relative_to(parent.resolve())
        return True
    except ValueError:
        return False


def prepare_managed_directory(root: Path, relative: Path) -> Path:
    current = root
    for part in relative.parts:
        current = current / part
        if os.path.lexists(current):
            if not current.is_dir() or is_reparse(current):
                raise CleanupRejected("UNSAFE_MANAGED_DIRECTORY")
        else:
            current.mkdir()
        if not contained(current, root):
            raise CleanupRejected("UNSAFE_MANAGED_DIRECTORY")
    return current


def validate_managed_directory(root: Path, relative: Path) -> Path:
    current = root
    for part in relative.parts:
        current = current / part
        if (
            not current.is_dir()
            or is_reparse(current)
            or not contained(current, root)
        ):
            raise CleanupRejected("UNSAFE_MANAGED_DIRECTORY")
    return current


def validate_catalog_path(path: Path) -> None:
    if (
        not path.is_file()
        or is_reparse(path)
        or path.resolve().parent != path.parent.resolve()
    ):
        raise CleanupRejected("UNSAFE_CATALOG_PATH")


def _valid_catalog_entry(project_id: Any, row: Any) -> bool:
    if not isinstance(project_id, str) or not PROJECT_ID.fullmatch(project_id):
        return False
    if not isinstance(row, dict):
        return False
    if not isinstance(row.get("name"), str) or not row["name"].strip():
        return False
    if not isinstance(row.get("remote"), str):
        return False
    aliases = row.get("path_aliases")
    if not isinstance(aliases, list) or not aliases:
        return False
    if any(
        not isinstance(alias, str)
        or not alias
        or not Path(alias).is_absolute()
        for alias in aliases
    ):
        return False
    for field in ("last_accessed", "tracking_state"):
        if field in row and not isinstance(row[field], str):
            return False
    return True


def read_catalog(path: Path) -> tuple[dict[str, Any], bytes]:
    validate_catalog_path(path)
    raw = path.read_bytes()
    catalog = json.loads(raw.decode("utf-8"))
    if (
        not isinstance(catalog, dict)
        or catalog.get("schema_version") != SCHEMA_VERSION
        or not isinstance(catalog.get("projects"), dict)
    ):
        raise CleanupRejected("INVALID_CATALOG_SCHEMA")
    if any(
        not isinstance(project_id, str)
        or not PROJECT_ID.fullmatch(project_id)
        for project_id in catalog["projects"]
    ):
        raise CleanupRejected("INVALID_PROJECT_ID")
    if any(
        not _valid_catalog_entry(project_id, row)
        for project_id, row in catalog["projects"].items()
    ):
        raise CleanupRejected("INVALID_CATALOG_ENTRY")
    return catalog, raw


def anchor_projects(
    root: Path,
    projects: dict[str, Any],
) -> set[str]:
    result: set[str] = set()
    sessions = root / "sessions"
    if not sessions.exists():
        return result
    if not sessions.is_dir() or is_reparse(sessions):
        raise CleanupRejected("UNSAFE_SESSIONS_DIRECTORY")
    for path in sessions.glob("*/anchor.json"):
        if is_reparse(path) or not contained(path, sessions):
            raise CleanupRejected("UNSAFE_ANCHOR_PATH")
        try:
            anchor = load_json(path, {})
        except (OSError, json.JSONDecodeError) as exc:
            raise CleanupRejected("INVALID_ANCHOR") from exc
        if not isinstance(anchor, dict) or anchor.get("schema_version") != 1:
            raise CleanupRejected("INVALID_ANCHOR")
        project_id = anchor.get("project_id")
        if not isinstance(project_id, str) or not project_id:
            raise CleanupRejected("INVALID_ANCHOR")
        if project_id not in projects:
            raise CleanupRejected("ANCHOR_CATALOG_MISMATCH")
        result.add(project_id)
    return result


def alias_key(alias: str) -> str:
    return os.path.normcase(os.path.abspath(alias))


def shared_aliases(projects: dict[str, Any]) -> set[str]:
    owners: dict[str, set[str]] = {}
    for project_id, row in projects.items():
        aliases = row.get("path_aliases") if isinstance(row, dict) else None
        if not isinstance(aliases, list):
            continue
        for alias in aliases:
            if isinstance(alias, str) and alias:
                owners.setdefault(alias_key(alias), set()).add(project_id)
    return {alias for alias, values in owners.items() if len(values) > 1}


def _manifest_reasons(
    project: Path,
    project_id: str,
    aliases: list[str],
    remote: str,
) -> list[str]:
    path = project / "manifest.json"
    if not path.is_file() or is_reparse(path):
        return ["MANIFEST_REQUIRED"]
    try:
        manifest = load_json(path, {})
    except (OSError, json.JSONDecodeError):
        return ["INVALID_MANIFEST"]
    if not isinstance(manifest, dict):
        return ["INVALID_MANIFEST"]
    if manifest.get("schema_version") != SCHEMA_VERSION:
        return ["INVALID_MANIFEST_SCHEMA"]
    reasons: list[str] = []
    alias_keys = {alias_key(alias) for alias in aliases}
    for field in ("identity", "indexed_identity"):
        identity = manifest.get(field)
        if not isinstance(identity, dict):
            reasons.append("MANIFEST_IDENTITY_MISMATCH")
            continue
        if identity.get("id") != project_id:
            reasons.append("MANIFEST_IDENTITY_MISMATCH")
        root = identity.get("root")
        if not isinstance(root, str) or alias_key(root) not in alias_keys:
            reasons.append("MANIFEST_IDENTITY_MISMATCH")
        if identity.get("remote") != remote:
            reasons.append("MANIFEST_IDENTITY_MISMATCH")
    return sorted(set(reasons))


def assess_project(
    root: Path,
    project_id: str,
    row: Any,
    anchored: set[str],
    shared: set[str],
) -> tuple[Path, list[str]]:
    reasons: list[str] = []
    if not isinstance(row, dict):
        return root / "projects" / project_id, ["INVALID_CATALOG_ENTRY"]
    remote = row.get("remote")
    if not isinstance(remote, str):
        reasons.append("INVALID_REMOTE")
        remote = ""
    elif remote != "":
        reasons.append("REMOTE_PRESENT")
    aliases = row.get("path_aliases")
    valid_aliases = isinstance(aliases, list) and all(
        isinstance(alias, str) and alias and Path(alias).is_absolute()
        for alias in aliases
    )
    if not valid_aliases:
        reasons.append("INVALID_PATH_ALIASES")
        aliases = []
    else:
        normalized = {alias_key(alias) for alias in aliases}
        if normalized & shared:
            reasons.append("SHARED_PATH_ALIAS")
        if any(os.path.lexists(alias) for alias in aliases):
            reasons.append("PATH_ALIAS_EXISTS")
    if project_id in anchored:
        reasons.append("SESSION_ANCHORED")
    projects = root / "projects"
    project = projects / project_id
    safe_project = (
        projects.is_dir()
        and not is_reparse(projects)
        and contained(projects, root)
        and project.is_dir()
        and not is_reparse(project)
        and project.resolve().parent == projects.resolve()
    )
    if not safe_project:
        reasons.append("UNSAFE_PROJECT_DIRECTORY")
    elif valid_aliases and isinstance(remote, str):
        reasons.extend(
            _manifest_reasons(project, project_id, aliases, remote)
        )
    return project, sorted(set(reasons))


def batch_directory(root: Path, batch_id: str) -> Path:
    base = prepare_managed_directory(
        root, Path(".backups") / "orphan-projects"
    )
    batch = base / batch_id
    if os.path.lexists(batch):
        raise CleanupRejected("BACKUP_TARGET_EXISTS")
    return batch


def validate_project_source(root: Path, source: Path) -> None:
    projects = validate_managed_directory(root, Path("projects"))
    if (
        not source.is_dir()
        or is_reparse(source)
        or source.resolve().parent != projects.resolve()
    ):
        raise CleanupRejected("UNSAFE_PROJECT_DIRECTORY")


def validate_missing_source(root: Path, source: Path) -> None:
    projects = validate_managed_directory(root, Path("projects"))
    if (
        os.path.lexists(source)
        or source.resolve().parent != projects.resolve()
    ):
        raise CleanupRejected("UNSAFE_PROJECT_DIRECTORY")


def validate_batch(root: Path, batch: Path) -> Path:
    base = validate_managed_directory(
        root, Path(".backups") / "orphan-projects"
    )
    if (
        not batch.is_dir()
        or is_reparse(batch)
        or batch.resolve().parent != base.resolve()
    ):
        raise CleanupRejected("UNSAFE_BACKUP_DIRECTORY")
    projects = batch / "projects"
    if (
        not projects.is_dir()
        or is_reparse(projects)
        or projects.resolve().parent != batch.resolve()
    ):
        raise CleanupRejected("UNSAFE_BACKUP_DIRECTORY")
    return projects


def validate_backup_path(
    root: Path, batch: Path, backup: Path, *, exists: bool
) -> None:
    projects = validate_batch(root, batch)
    safe_parent = backup.resolve().parent == projects.resolve()
    if exists:
        safe = backup.is_dir() and not is_reparse(backup) and safe_parent
    else:
        safe = not os.path.lexists(backup) and safe_parent
    if not safe:
        raise CleanupRejected("UNSAFE_BACKUP_DIRECTORY")
