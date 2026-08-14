from __future__ import annotations

import argparse
from contextlib import ExitStack, contextmanager
import json
import re
import uuid
from pathlib import Path
from typing import Any, Iterator

from file_lock import exclusive_file_lock
from orphan_cleanup_transaction import apply_transaction
from orphan_cleanup_validation import (
    CleanupRejected,
    anchor_projects,
    assess_project,
    batch_directory,
    prepare_managed_directory,
    read_catalog,
    shared_aliases,
    validate_managed_directory,
)
from rag_common import utc_now
from rag_locks import maintenance_lock, project_id_lock


PROJECT_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*\Z")
BATCH_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*\Z")


def _project_ids(
    projects: dict[str, Any], requested: list[str] | None, apply: bool
) -> list[str]:
    if requested:
        _validate_requested(requested)
        unknown = [value for value in requested if value not in projects]
        if unknown:
            raise CleanupRejected("UNKNOWN_PROJECT_ID")
        return requested
    if apply:
        raise CleanupRejected("PROJECT_ID_REQUIRED")
    selected = sorted(projects)
    if any(not PROJECT_ID.fullmatch(value) for value in selected):
        raise CleanupRejected("INVALID_PROJECT_ID")
    return selected


def _validate_requested(requested: list[str]) -> None:
    if len(requested) != len(set(requested)):
        raise CleanupRejected("DUPLICATE_PROJECT_ID")
    if any(not PROJECT_ID.fullmatch(value) for value in requested):
        raise CleanupRejected("INVALID_PROJECT_ID")


@contextmanager
def _ordered_locks(
    root: Path,
    catalog_path: Path,
    requested: list[str] | None,
    apply: bool,
) -> Iterator[list[str]]:
    if requested:
        _validate_requested(requested)
        selected = requested
    else:
        with exclusive_file_lock(catalog_path.with_suffix(".lock")):
            preview, _ = read_catalog(catalog_path)
            selected = _project_ids(preview["projects"], None, apply)
    prepare_managed_directory(root, Path(".locks"))
    with ExitStack() as stack:
        for project_id in sorted(selected):
            stack.enter_context(
                exclusive_file_lock(project_id_lock(root, project_id))
            )
        stack.enter_context(exclusive_file_lock(maintenance_lock(root)))
        stack.enter_context(
            exclusive_file_lock(catalog_path.with_suffix(".lock"))
        )
        validate_managed_directory(root, Path(".locks"))
        yield selected


def cleanup_orphan_projects(
    knowledge_root: Path,
    project_ids: list[str] | None = None,
    *,
    apply: bool = False,
    yes: bool = False,
    maintenance_window: bool = False,
    batch_id: str | None = None,
) -> dict[str, Any]:
    root = knowledge_root.resolve()
    catalog_path = root / "catalog.json"
    if apply and not project_ids:
        raise CleanupRejected("PROJECT_ID_REQUIRED")
    if apply and not yes:
        raise CleanupRejected("YES_REQUIRED")
    if apply and not maintenance_window:
        raise CleanupRejected("MAINTENANCE_WINDOW_REQUIRED")
    with _ordered_locks(root, catalog_path, project_ids, apply) as locked:
        catalog, catalog_bytes = read_catalog(catalog_path)
        projects = catalog["projects"]
        selected = _project_ids(projects, project_ids, apply)
        if selected != locked:
            raise CleanupRejected("CATALOG_CHANGED_RETRY")
        anchored = anchor_projects(root, projects)
        shared = shared_aliases(projects)
        checks: list[dict[str, Any]] = []
        sources: dict[str, Path] = {}
        for project_id in selected:
            source, reasons = assess_project(
                root,
                project_id,
                projects[project_id],
                anchored,
                shared,
            )
            sources[project_id] = source
            checks.append(
                {
                    "project_id": project_id,
                    "eligible": not reasons,
                    "reasons": reasons,
                }
            )
        eligible = [row["project_id"] for row in checks if row["eligible"]]
        if not apply:
            return {
                "status": "DRY_RUN",
                "apply": False,
                "projects": checks,
                "eligible_count": len(eligible),
            }
        if len(eligible) != len(selected):
            raise CleanupRejected("PROJECT_NOT_ELIGIBLE")
        chosen_batch = batch_id or (
            utc_now().replace(":", "").replace("+", "-")
            + "-"
            + uuid.uuid4().hex[:12]
        )
        if not BATCH_ID.fullmatch(chosen_batch):
            raise CleanupRejected("INVALID_BATCH_ID")
        batch = batch_directory(root, chosen_batch)
        return apply_transaction(
            catalog_path,
            catalog,
            catalog_bytes,
            sources,
            batch,
            selected,
        )


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Safely back up and remove orphan project caches."
    )
    parser.add_argument("--knowledge-root", required=True, type=Path)
    parser.add_argument("--project-id", action="append", dest="project_ids")
    parser.add_argument("--apply", action="store_true")
    parser.add_argument("--yes", action="store_true")
    parser.add_argument("--maintenance-window", action="store_true")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        receipt = cleanup_orphan_projects(
            args.knowledge_root,
            args.project_ids,
            apply=args.apply,
            yes=args.yes,
            maintenance_window=args.maintenance_window,
        )
    except CleanupRejected as exc:
        receipt = {"status": "REJECTED", "reason": str(exc)}
        print(json.dumps(receipt, ensure_ascii=False))
        return 2
    except json.JSONDecodeError:
        receipt = {"status": "REJECTED", "reason": "INVALID_JSON"}
        print(json.dumps(receipt, ensure_ascii=False))
        return 2
    except OSError:
        receipt = {"status": "REJECTED", "reason": "IO_ERROR"}
        print(json.dumps(receipt, ensure_ascii=False))
        return 2
    except RuntimeError as exc:
        reason = str(exc)
        status = "ROLLBACK_FAILED" if reason.startswith(
            "ROLLBACK_FAILED"
        ) else "FAILED"
        print(json.dumps({"status": status, "reason": reason}))
        return 1
    print(json.dumps(receipt, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
