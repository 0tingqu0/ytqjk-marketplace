from __future__ import annotations

import hashlib
import os
import re
import stat
import subprocess
from pathlib import Path

from rag_security import is_sensitive_path, normalize_remote


def run_git(project: Path, *args: str, check: bool = True) -> str:
    result = subprocess.run(
        ["git", "-C", str(project), *args],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="surrogateescape",
        check=False,
    )
    if check and result.returncode != 0:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip())
    return result.stdout


def is_git_project(project: Path) -> bool:
    return run_git(
        project, "rev-parse", "--is-inside-work-tree", check=False
    ).strip() == "true"


def _safe_project_name(root: Path) -> str:
    return re.sub(r"[^a-zA-Z0-9._-]+", "-", root.name).strip("-_") or "project"


def project_identity(project: Path) -> dict[str, str]:
    """Return stable project identity without scanning source or calculating a diff."""
    project = project.resolve()
    if not project.is_dir():
        raise ValueError("项目工作目录不存在或不是目录。")
    if not is_git_project(project):
        name = _safe_project_name(project)
        identity = os.path.normcase(str(project))
        return {
            "id": f"{name}--{hashlib.sha256(identity.encode('utf-8')).hexdigest()[:12]}",
            "name": name,
            "remote": "",
            "root": str(project),
        }

    root = Path(run_git(project, "rev-parse", "--show-toplevel").strip()).resolve()
    raw_remote = run_git(root, "remote", "get-url", "origin", check=False).strip()
    common_value = Path(run_git(root, "rev-parse", "--git-common-dir").strip())
    common = (common_value if common_value.is_absolute() else root / common_value).resolve()
    canonical_root = common.parent if common.name.casefold() == ".git" else common
    normalized_remote = normalize_remote(raw_remote)
    identity = normalized_remote or os.path.normcase(str(canonical_root))
    name = _safe_project_name(canonical_root)
    short_hash = hashlib.sha256(identity.encode("utf-8")).hexdigest()[:12]
    project_id = "p2604_soc" if name.casefold() == "p2604_soc" else f"{name}--{short_hash}"
    return {
        "id": project_id,
        "name": name,
        "remote": normalized_remote,
        "root": str(root),
    }


def project_query_state(project: Path) -> dict[str, str]:
    """Read cheap freshness signals; never materialize a diff or walk source files."""
    if not is_git_project(project):
        return {"head": "NON_GIT", "dirty": "unknown"}
    root = Path(run_git(project, "rev-parse", "--show-toplevel").strip()).resolve()
    head = run_git(root, "rev-parse", "--verify", "HEAD", check=False).strip() or "UNBORN"
    dirty = str(bool(run_git(root, "status", "--short", "--untracked-files=no").strip())).lower()
    materialized = subprocess.run(
        ["git", "-C", str(root), "ls-files", "-t", "-z"],
        capture_output=True,
        check=True,
    ).stdout
    return {
        "head": head,
        "dirty": dirty,
        "materialization": hashlib.sha256(materialized).hexdigest(),
    }


def tracked_paths(project: Path, excluded_root: Path | None = None) -> list[str]:
    project = project.resolve()
    excluded = excluded_root.resolve() if excluded_root else None
    if is_git_project(project):
        output = subprocess.run(
            ["git", "-C", str(project), "ls-files", "-z"],
            capture_output=True,
            check=True,
        ).stdout
        paths = [
            part.decode("utf-8", errors="strict")
            for part in output.split(b"\0")
            if part
        ]
        return [
            path
            for path in paths
            if _is_safe_project_path(project, project / path)
            and not _is_excluded(project / path, excluded)
        ]

    paths: list[str] = []
    for directory, names, filenames in os.walk(project, topdown=True, followlinks=False):
        current = Path(directory)
        names[:] = [
            name
            for name in names
            if _is_safe_project_path(project, current / name)
            and not is_sensitive_path((current / name).relative_to(project).as_posix())
            and not _is_excluded(current / name, excluded)
        ]
        for name in filenames:
            path = current / name
            relative = path.relative_to(project).as_posix()
            if (
                _is_safe_project_path(project, path)
                and not is_sensitive_path(relative)
                and not _is_excluded(path, excluded)
            ):
                paths.append(relative)
    return sorted(paths)


def _is_safe_project_path(project: Path, path: Path) -> bool:
    try:
        if path.is_symlink() or _is_junction(path):
            return False
        return path.resolve(strict=True).is_relative_to(project)
    except (OSError, RuntimeError):
        return False


def _is_junction(path: Path) -> bool:
    checker = getattr(path, "is_junction", None)
    if checker is not None:
        return bool(checker())
    attributes = getattr(os.lstat(path), "st_file_attributes", 0)
    reparse = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0)
    return bool(attributes & reparse)


def _is_excluded(path: Path, excluded_root: Path | None) -> bool:
    if excluded_root is None:
        return False
    try:
        common = os.path.commonpath(
            (os.path.normcase(path.resolve()), os.path.normcase(excluded_root))
        )
        return common == os.path.normcase(excluded_root)
    except ValueError:
        return False
