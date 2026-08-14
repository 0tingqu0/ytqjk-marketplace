from __future__ import annotations

import os
import shutil
import tempfile
from pathlib import Path
from typing import Any

from orphan_cleanup_validation import (
    CleanupRejected,
    validate_backup_path,
    validate_batch,
    validate_catalog_path,
    validate_managed_directory,
    validate_missing_source,
    validate_project_source,
)
from rag_common import atomic_json, utc_now


def _atomic_bytes(path: Path, content: bytes) -> None:
    descriptor, name = tempfile.mkstemp(
        dir=path.parent, prefix=f"{path.name}.", suffix=".tmp"
    )
    temporary = Path(name)
    try:
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def _rollback(
    root: Path,
    catalog_path: Path,
    catalog_bytes: bytes,
    moved: list[tuple[Path, Path]],
    batch: Path,
    catalog_touched: bool,
) -> None:
    failures: list[str] = []
    if catalog_touched:
        try:
            validate_catalog_path(catalog_path)
            _atomic_bytes(catalog_path, catalog_bytes)
            validate_catalog_path(catalog_path)
        except (CleanupRejected, OSError):
            failures.append("catalog")
    for source, backup in reversed(moved):
        try:
            if os.path.lexists(source):
                failures.append(f"source-conflict:{source.name}")
                continue
            if not os.path.lexists(backup):
                failures.append(f"backup-missing:{source.name}")
                continue
            validate_missing_source(root, source)
            validate_backup_path(root, batch, backup, exists=True)
            os.replace(backup, source)
            validate_project_source(root, source)
        except (CleanupRejected, OSError):
            failures.append(source.name)
    if failures:
        raise RuntimeError("ROLLBACK_FAILED:" + ",".join(failures))


def _same_directory(path: Path, expected: os.stat_result) -> bool:
    try:
        current = path.stat(follow_symlinks=False)
    except OSError:
        return False
    return os.path.samestat(current, expected)


def _cleanup_owned_batch(
    root: Path, batch: Path, expected: os.stat_result | None
) -> None:
    if not os.path.lexists(batch):
        return
    try:
        validate_batch(root, batch)
        if expected is None or not _same_directory(batch, expected):
            raise CleanupRejected("BATCH_OWNERSHIP_CHANGED")
        shutil.rmtree(batch)
    except (CleanupRejected, OSError) as exc:
        raise RuntimeError("ROLLBACK_FAILED:batch-cleanup") from exc


def apply_transaction(
    catalog_path: Path,
    catalog: dict[str, Any],
    catalog_bytes: bytes,
    sources: dict[str, Path],
    batch: Path,
    project_ids: list[str],
) -> dict[str, Any]:
    root = catalog_path.parent
    project_backups = batch / "projects"
    moved: list[tuple[Path, Path]] = []
    catalog_touched = False
    batch_owned = False
    batch_identity: os.stat_result | None = None
    receipt = {
        "status": "APPLIED",
        "apply": True,
        "batch_id": batch.name,
        "project_ids": project_ids,
        "removed_count": len(project_ids),
        "created_at": utc_now(),
    }
    plan = {
        "status": "PLANNED",
        "batch_id": batch.name,
        "project_ids": project_ids,
        "project_count": len(project_ids),
        "action": "ISOLATE_WITHOUT_PURGE",
    }
    try:
        validate_managed_directory(
            root, Path(".backups") / "orphan-projects"
        )
        try:
            batch.mkdir()
        except FileExistsError as exc:
            raise CleanupRejected("BACKUP_TARGET_EXISTS") from exc
        batch_owned = True
        batch_identity = batch.stat(follow_symlinks=False)
        project_backups.mkdir()
        validate_batch(root, batch)
        _atomic_bytes(batch / "catalog.before.json", catalog_bytes)
        validate_batch(root, batch)
        atomic_json(batch / "plan.json", plan)
        validate_batch(root, batch)
        for project_id in project_ids:
            source = sources[project_id]
            target = project_backups / project_id
            validate_project_source(root, source)
            validate_backup_path(root, batch, target, exists=False)
            os.replace(source, target)
            moved.append((source, target))
            validate_backup_path(root, batch, target, exists=True)
        updated = dict(catalog)
        updated["projects"] = dict(catalog["projects"])
        for project_id in project_ids:
            del updated["projects"][project_id]
        catalog_touched = True
        validate_catalog_path(catalog_path)
        atomic_json(catalog_path, updated)
        validate_catalog_path(catalog_path)
        validate_batch(root, batch)
        atomic_json(batch / "receipt.json", receipt)
        validate_batch(root, batch)
        return receipt
    except BaseException:
        try:
            _rollback(
                root,
                catalog_path,
                catalog_bytes,
                moved,
                batch,
                catalog_touched,
            )
        except RuntimeError:
            raise
        if batch_owned:
            _cleanup_owned_batch(root, batch, batch_identity)
        raise
