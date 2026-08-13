"""Migration port protocol and in-memory fixture implementation."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Protocol

from .migration_plan import ManifestEntry, MigrationSafetyError


@dataclass(frozen=True)
class StageInfo:
    stage_id: str
    active: dict[str, int]
    completed_receipt: dict[str, object] | None = None


class MigrationPort(Protocol):
    """Migration boundary. Adapters never open or query target SQLite."""

    def begin_stage(self, fingerprint: str) -> StageInfo: ...
    def import_document(
        self, stage_id: str, entry: ManifestEntry, content: bytes
    ) -> str: ...
    def import_anchor(self, stage_id: str, anchor: dict[str, str]) -> None: ...
    def readback(self, stage_id: str) -> dict[str, object]: ...
    def citation_matches(
        self, stage_id: str, project: str, query: str
    ) -> tuple[str, ...]: ...
    def verify(self, stage_id: str, evidence: dict[str, object]) -> bool: ...
    def activate_cas(
        self, stage_id: str, active: dict[str, object]
    ) -> bool: ...
    def active_readback(self) -> dict[str, object]: ...
    def abort_stage(self, stage_id: str) -> bool: ...
    def stage_exists(self, stage_id: str) -> bool: ...
    def stage_artifacts(self, stage_id: str) -> dict[str, object]: ...
    def record_receipt(
        self, fingerprint: str, receipt: dict[str, object]
    ) -> bool: ...


@dataclass
class _Document:
    digest: str
    project: str
    sources: set[str] = field(default_factory=set)
    governance: dict[str, str] = field(default_factory=dict)
    owners: dict[str, set[str]] = field(default_factory=dict)
    content: bytes = b""


@dataclass
class _Stage:
    fingerprint: str
    active: dict[str, int]
    documents: set[str] = field(default_factory=set)
    sources: dict[str, set[str]] = field(default_factory=dict)
    anchors: set[tuple[str, str, str]] = field(default_factory=set)


class InMemoryMigrationPort:
    """Fixture port with staged resume, readback, CAS, verified abort."""

    fixture_only = True
    production_ready = False
    adapter_name = "IN_MEMORY_FIXTURE"

    def __init__(self) -> None:
        self.documents: dict[str, _Document] = {}
        self.stages: dict[str, _Stage] = {}
        self.active: dict[str, int] = {}
        self.snapshots: dict[str, dict[str, object]] = {}
        self.receipts: dict[str, dict[str, object]] = {}
        self.committed: set[str] = set()

    def begin_stage(self, fingerprint: str) -> StageInfo:
        completed = self.receipts.get(fingerprint)
        if completed is not None:
            return StageInfo(fingerprint, dict(self.active), completed)
        if fingerprint in self.committed:
            return StageInfo(
                fingerprint,
                dict(self.active),
                {"status": "BLOCKED", "reason": "RECEIPT_PERSISTENCE_UNKNOWN"},
            )
        stage = self.stages.setdefault(
            fingerprint, _Stage(fingerprint, dict(self.active))
        )
        return StageInfo(stage.fingerprint, dict(stage.active))

    def import_document(
        self, stage_id: str, entry: ManifestEntry, content: bytes
    ) -> str:
        document_id = f"{entry.project_alias}:{entry.sha256}"
        document = self.documents.setdefault(
            document_id,
            _Document(entry.sha256, entry.project_alias, content=content),
        )
        if document.project != entry.project_alias:
            raise MigrationSafetyError(
                "content digest has conflicting ownership"
            )
        document.sources.add(entry.relative_path)
        document.governance[entry.relative_path] = entry.state
        document.owners.setdefault(entry.relative_path, set()).add(stage_id)
        self.stages[stage_id].documents.add(document_id)
        self.stages[stage_id].sources.setdefault(document_id, set()).add(
            entry.relative_path
        )
        return document_id

    def import_anchor(self, stage_id: str, anchor: dict[str, str]) -> None:
        self.stages[stage_id].anchors.add(
            (
                anchor["legacy_project"],
                anchor["project_alias"],
                anchor["session_key"],
            )
        )

    def readback(self, stage_id: str) -> dict[str, object]:
        stage = self.stages[stage_id]
        documents = {
            doc_id: {
                "digest": self.documents[doc_id].digest,
                "project": self.documents[doc_id].project,
                "sources": sorted(self.documents[doc_id].sources),
                "governance": dict(self.documents[doc_id].governance),
            }
            for doc_id in stage.documents
        }
        memberships = {
            project: sorted(
                doc_id
                for doc_id in stage.documents
                if self.documents[doc_id].project == project
            )
            for project in {
                self.documents[doc_id].project for doc_id in stage.documents
            }
        }
        return {
            "documents": documents,
            "memberships": memberships,
            "anchors": sorted(stage.anchors),
        }

    def citation_matches(
        self, stage_id: str, project: str, query: str
    ) -> tuple[str, ...]:
        stage = self.stages[stage_id]
        return tuple(
            doc_id
            for doc_id in sorted(stage.documents)
            if self.documents[doc_id].project == project
            and query in self.documents[doc_id].content.decode("utf-8")
        )

    def verify(self, stage_id: str, evidence: dict[str, object]) -> bool:
        required = {"digests", "governance", "citations", "anchors"}
        return self.stage_exists(stage_id) and required <= set(evidence)

    def activate_cas(self, stage_id: str, active: dict[str, object]) -> bool:
        stage = self.stages.get(stage_id)
        if stage is None or self.active_readback() != active:
            return False
        projects = {
            self.documents[doc_id].project for doc_id in stage.documents
        }
        self.active.update(
            {project: self.active.get(project, 0) + 1 for project in projects}
        )
        for project in projects:
            members = {
                doc_id: self.documents[doc_id].digest
                for doc_id in stage.documents
                if self.documents[doc_id].project == project
            }
            generation = self.active[project]
            snapshot_id = f"{stage_id}:{project}:{generation}"
            self.snapshots[project] = {
                "pointer": snapshot_id,
                "snapshot_id": snapshot_id,
                "generation": generation,
                "membership": members,
            }
        for doc_id, sources in stage.sources.items():
            for source in sources:
                document = self.documents[doc_id]
                document.owners[source].discard(stage_id)
                document.owners[source].add(f"active:{stage.fingerprint}")
        del self.stages[stage_id]
        self.committed.add(stage.fingerprint)
        return True

    def active_readback(self) -> dict[str, object]:
        return {
            "projects": {
                key: dict(value) for key, value in self.snapshots.items()
            }
        }

    def abort_stage(self, stage_id: str) -> bool:
        stage = self.stages.pop(stage_id, None)
        if stage is None:
            return False
        for doc_id, sources in stage.sources.items():
            document = self.documents[doc_id]
            for source in sources:
                owners = document.owners[source]
                owners.discard(stage_id)
                if not owners:
                    del document.owners[source]
                    document.sources.remove(source)
                    del document.governance[source]
            if not document.sources:
                del self.documents[doc_id]
        return True

    def stage_exists(self, stage_id: str) -> bool:
        return stage_id in self.stages

    def stage_artifacts(self, stage_id: str) -> dict[str, object]:
        stage = self.stages.get(stage_id)
        if stage is None:
            return {"documents": {}, "anchors": [], "citations": []}
        documents = {
            doc_id: {
                "digest": self.documents[doc_id].digest,
                "sources": sorted(sources),
            }
            for doc_id, sources in stage.sources.items()
        }
        return {
            "documents": documents,
            "anchors": sorted(stage.anchors),
            "citations": [],
        }

    def record_receipt(
        self, fingerprint: str, receipt: dict[str, object]
    ) -> bool:
        if fingerprint not in self.committed:
            return False
        self.receipts[fingerprint] = dict(receipt)
        return True
