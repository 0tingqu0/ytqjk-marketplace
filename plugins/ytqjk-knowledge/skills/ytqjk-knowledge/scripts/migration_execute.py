"""Staged migration executor. Port owns target mutation and all readback."""

from __future__ import annotations

import hashlib
from pathlib import Path
from typing import Any

from .migration_plan import (
    ManifestEntry,
    MigrationPlan,
    MigrationSafetyError,
    verify_source,
)
from .migration_port import MigrationPort
from .migration_verify import validate_stage


_CAPABILITIES = (
    "begin_stage", "import_document", "import_anchor", "readback",
    "citation_matches", "verify", "activate_cas", "active_readback",
    "abort_stage", "stage_exists", "stage_artifacts", "record_receipt",
)


def execute(
    plan: MigrationPlan,
    port: MigrationPort,
    *,
    apply: bool = False,
    confirmation_token: str | None = None,
    allow_fixture: bool = False,
) -> dict[str, object]:
    """Dry-run default. Production apply rejects fixture ports by default."""
    receipt = _receipt(plan, port)
    if not apply:
        return {**receipt, "mode": "DRY_RUN", "switch": "NOT_ATTEMPTED"}
    if confirmation_token != plan.fingerprint:
        raise MigrationSafetyError(
            "apply requires matching plan fingerprint token"
        )
    missing = _missing_capabilities(port)
    if missing:
        return _blocked(
            receipt,
            "PORT_CAPABILITY_MISSING",
            missing_capabilities=missing,
        )
    fixture = getattr(port, "fixture_only", None) is True
    if fixture and not allow_fixture:
        return {
            **receipt,
            "mode": "APPLY",
            "status": "NOT_CONFIGURED",
            "reason": "FIXTURE_ADAPTER_REQUIRES_ALLOW_FIXTURE",
            "switch": "NOT_ATTEMPTED",
            "rollback": "NOT_ATTEMPTED",
        }
    if not fixture and getattr(port, "production_ready", None) is not True:
        return {
            **receipt,
            "mode": "APPLY",
            "status": "NOT_CONFIGURED",
            "reason": "PRODUCTION_ADAPTER_NOT_READY",
            "switch": "NOT_ATTEMPTED",
            "rollback": "NOT_ATTEMPTED",
        }
    verify_source(plan)
    try:
        before = _active(port.active_readback())
    except Exception:
        return _blocked(receipt, "ACTIVE_READBACK_UNAVAILABLE")
    stage = port.begin_stage(plan.fingerprint)
    if stage.completed_receipt is not None:
        return stage.completed_receipt
    try:
        for entry in plan.entries:
            port.import_document(stage.stage_id, entry, _content(plan, entry))
        for anchor in plan.anchors:
            port.import_anchor(stage.stage_id, anchor)
        evidence = validate_stage(plan, port, stage.stage_id)
        if not port.verify(stage.stage_id, evidence):
            raise MigrationSafetyError(
                "port verification unavailable or rejected"
            )
        if not port.activate_cas(stage.stage_id, before):
            raise MigrationSafetyError("active snapshot CAS failed")
        after = _active(port.active_readback())
        if not _active_matches(before, after, evidence):
            return {
                **receipt,
                "mode": "APPLY",
                "status": "BLOCKED",
                "reason": "ACTIVE_READBACK_MISMATCH",
                "switch": "UNKNOWN",
                "rollback": "NOT_ATTEMPTED",
            }
        completed = {
            **receipt,
            "mode": "APPLY",
            "status": "COMPLETED",
            "switch": "ACTIVE",
            "rollback": "NOT_NEEDED",
            "verification": {**evidence, "active_snapshot": "MATCHED"},
            "active_generations": _generations(after),
        }
        if not port.record_receipt(plan.fingerprint, completed):
            return {
                **receipt,
                "mode": "APPLY",
                "status": "BLOCKED",
                "reason": "RECEIPT_PERSISTENCE_UNKNOWN",
                "switch": "UNKNOWN",
                "rollback": "NOT_ATTEMPTED",
            }
        return completed
    except Exception as error:
        rollback = _rollback(port, stage.stage_id, before)
        return {
            **receipt,
            "mode": "APPLY",
            "status": (
                "BLOCKED"
                if isinstance(error, MigrationSafetyError)
                else "FAILED"
            ),
            "reason": type(error).__name__,
            "switch": "FAILED",
            "rollback": "SUCCEEDED" if rollback else "FAILED",
        }


def _receipt(plan: MigrationPlan, port: object) -> dict[str, object]:
    fixture = getattr(port, "fixture_only", None) is True
    production_ready = getattr(port, "production_ready", None) is True
    return {
        **plan.receipt(),
        "adapter": str(getattr(port, "adapter_name", "UNCONFIGURED")),
        "production_apply": not fixture and production_ready,
        "target_database_write": (
            "NOT_CONFIGURED" if fixture else "PORT_MANAGED"
        ),
    }


def _missing_capabilities(port: object) -> tuple[str, ...]:
    missing: list[str] = []
    for name in _CAPABILITIES:
        try:
            available = callable(getattr(port, name))
        except Exception:
            available = False
        if not available:
            missing.append(name)
    return tuple(missing)


def _blocked(
    receipt: dict[str, object], reason: str, **extra: object
) -> dict[str, object]:
    return {
        **receipt,
        "mode": "APPLY",
        "status": "BLOCKED",
        "reason": reason,
        "switch": "NOT_ATTEMPTED",
        "rollback": "NOT_ATTEMPTED",
        **extra,
    }


def _rollback(
    port: MigrationPort, stage_id: str, before: dict[str, object]
) -> bool:
    try:
        aborted = port.abort_stage(stage_id)
        artifacts = port.stage_artifacts(stage_id)
        after = _active(port.active_readback())
        return (
            aborted
            and not port.stage_exists(stage_id)
            and _empty(artifacts)
            and after == before
        )
    except Exception:
        return False


def _empty(artifacts: object) -> bool:
    if not isinstance(artifacts, dict):
        return False
    return (
        artifacts.get("documents") == {}
        and artifacts.get("anchors") == []
        and artifacts.get("citations") == []
    )


def _active(value: object) -> dict[str, object]:
    if not isinstance(value, dict) or not isinstance(
        value.get("projects"), dict
    ):
        raise MigrationSafetyError("active readback is malformed")
    return value


def _active_matches(
    before: dict[str, object],
    after: dict[str, object],
    evidence: dict[str, object],
) -> bool:
    prior, current = before["projects"], after["projects"]
    if not isinstance(prior, dict) or not isinstance(current, dict):
        return False
    memberships = evidence["memberships"]
    documents = evidence["documents"]
    if not isinstance(memberships, dict) or not isinstance(documents, dict):
        return False
    if set(current) != set(prior) | set(memberships):
        return False
    for project, expected_ids in memberships.items():
        row = current.get(project)
        previous = prior.get(project, {})
        if not isinstance(row, dict) or not isinstance(previous, dict):
            return False
        pointer = row.get("pointer")
        if (
            not isinstance(pointer, str)
            or not pointer
            or pointer != row.get("snapshot_id")
        ):
            return False
        generation = row.get("generation")
        if generation != int(previous.get("generation", 0)) + 1:
            return False
        if not isinstance(expected_ids, list) or not isinstance(
            row.get("membership"), dict
        ):
            return False
        expected = {item: documents[item]["digest"] for item in expected_ids}
        if row["membership"] != expected:
            return False
    return all(
        current[key] == value
        for key, value in prior.items()
        if key not in memberships
    )


def _generations(active: dict[str, object]) -> dict[str, int]:
    projects = active["projects"]
    assert isinstance(projects, dict)
    return {
        key: int(value["generation"])
        for key, value in projects.items()
        if isinstance(value, dict)
    }


def _content(plan: MigrationPlan, entry: ManifestEntry) -> bytes:
    path = Path(plan.source_root) / entry.relative_path
    try:
        content, metadata = path.read_bytes(), path.stat(follow_symlinks=False)
    except OSError as error:
        raise MigrationSafetyError(
            "legacy source unreadable during apply"
        ) from error
    if (
        len(content) != entry.size_bytes
        or metadata.st_mtime_ns != entry.modified_ns
    ):
        raise MigrationSafetyError("legacy source changed during apply")
    if hashlib.sha256(content).hexdigest() != entry.sha256:
        raise MigrationSafetyError("legacy source digest changed during apply")
    return content
