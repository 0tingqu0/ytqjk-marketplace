"""Readback validation for staged legacy migrations."""

from __future__ import annotations

import hashlib
from pathlib import Path

from .migration_plan import ManifestEntry, MigrationPlan, MigrationSafetyError
from .migration_port import MigrationPort


def validate_stage(
    plan: MigrationPlan, port: MigrationPort, stage_id: str
) -> dict[str, object]:
    evidence = port.readback(stage_id)
    documents = evidence.get("documents")
    memberships = evidence.get("memberships")
    anchors = evidence.get("anchors")
    if not isinstance(documents, dict) or not isinstance(memberships, dict):
        raise MigrationSafetyError("readback capability is incomplete")
    if not isinstance(anchors, list):
        raise MigrationSafetyError("readback capability is incomplete")
    _documents(plan.entries, documents)
    _memberships(plan.entries, memberships)
    expected = sorted(
        (item["legacy_project"], item["project_alias"], item["session_key"])
        for item in plan.anchors
    )
    if expected != anchors:
        raise MigrationSafetyError("anchor mapping readback failed")
    citations = _citations(plan, port, stage_id, documents)
    return {
        "documents": documents,
        "memberships": memberships,
        "counts": {
            "source_entries": len(plan.entries),
            "unique_documents": len(documents),
        },
        "digests": "MATCHED",
        "governance": "PRESERVED",
        "project_isolation": "PASSED",
        "snapshot_membership": "STAGED",
        "anchors": "MAPPED",
        "citations": citations,
    }


def _documents(
    entries: tuple[ManifestEntry, ...], documents: dict[object, object]
) -> None:
    expected: dict[str, list[ManifestEntry]] = {}
    for entry in entries:
        document_id = f"{entry.project_alias}:{entry.sha256}"
        expected.setdefault(document_id, []).append(entry)
    if set(documents) != set(expected):
        raise MigrationSafetyError("document inventory readback failed")
    for document_id, sources in expected.items():
        row = documents[document_id]
        paths = sorted(item.relative_path for item in sources)
        states = {item.relative_path: item.state for item in sources}
        if not isinstance(row, dict) or row.get("digest") != sources[0].sha256:
            raise MigrationSafetyError("digest or governance readback failed")
        if row.get("governance") != states or row.get("sources") != paths:
            raise MigrationSafetyError(
                "governance or provenance readback failed"
            )
        if row.get("project") != sources[0].project_alias:
            raise MigrationSafetyError("ownership readback failed")


def _memberships(
    entries: tuple[ManifestEntry, ...], memberships: dict[object, object]
) -> None:
    expected: dict[str, set[str]] = {}
    for entry in entries:
        expected.setdefault(entry.project_alias, set()).add(
            f"{entry.project_alias}:{entry.sha256}"
        )
    if {key: set(value) for key, value in memberships.items()} != expected:
        raise MigrationSafetyError("snapshot membership readback failed")


def _citations(
    plan: MigrationPlan,
    port: MigrationPort,
    stage_id: str,
    documents: dict[object, object],
) -> dict[str, str]:
    result: dict[str, str] = {}
    for entry in plan.entries:
        content = (Path(plan.source_root) / entry.relative_path).read_bytes()
        if hashlib.sha256(content).hexdigest() != entry.sha256:
            raise MigrationSafetyError("legacy source changed during citation")
        query = content.decode("utf-8").split(maxsplit=1)
        document_id = f"{entry.project_alias}:{entry.sha256}"
        matches = port.citation_matches(
            stage_id, entry.project_alias, query[0] if query else ""
        )
        if (
            not query
            or document_id not in documents
            or document_id not in matches
        ):
            raise MigrationSafetyError("fixed citation readback failed")
        result[entry.relative_path] = query[0]
    return result
