from __future__ import annotations

import json
import sqlite3
import sys
import uuid
from pathlib import Path
from typing import Callable

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.service import KnowledgeService  # noqa: E402


def _queued_project(service: KnowledgeService, alias: str) -> int:
    payload = {
        "id": str(uuid.uuid4()),
        "scope": "project",
        "alias": alias,
    }
    return service._queue.submit("create_project", payload)


def _mark_running(database: Path, job_id: int, lease: str) -> None:
    with sqlite3.connect(database) as connection:
        connection.execute(
            "UPDATE jobs SET state = 'RUNNING', owner = ?, heartbeat_at = ?, "
            "lease_expires_at = ?, started_at = ?, attempt = 1 WHERE id = ?",
            ("test-owner", "2026-08-17T00:00:00+00:00", lease,
             "2026-08-17T00:00:00+00:00", job_id),
        )
        connection.commit()


def _database_state(database: Path) -> tuple[object, ...]:
    with sqlite3.connect(database) as connection:
        version = connection.execute("PRAGMA user_version").fetchone()[0]
        schema = connection.execute(
            "SELECT type, name, tbl_name, sql FROM sqlite_master "
            "WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name"
        ).fetchall()
        jobs = connection.execute("SELECT * FROM jobs ORDER BY id").fetchall()
    return version, schema, jobs


def _knowledge_state(database: Path) -> dict[str, list[tuple[object, ...]]]:
    tables = (
        "projects", "originals", "documents", "versions", "chunks", "sources",
        "governance", "audit", "feedback_events", "global_sync",
    )
    with sqlite3.connect(database) as connection:
        return {
            table: connection.execute(
                f"SELECT * FROM {table} ORDER BY rowid"
            ).fetchall()
            for table in tables
        }


def _candidate(
    service: KnowledgeService, alias: str, *, scope: str = "project"
) -> tuple[str, str]:
    project_id = service.create_project(scope, alias)
    document_id = service.create_candidate(
        project_id, "knowledge", "content", "test"
    )
    return project_id, document_id


def _linked_mirror(
    service: KnowledgeService,
) -> tuple[str, str]:
    _, document_id = _candidate(service, "mirror-source")
    for _ in range(2):
        service.record_feedback(document_id, str(uuid.uuid4()), True)
    with sqlite3.connect(service._database) as connection:
        global_id = connection.execute(
            "SELECT global_document_id FROM global_sync "
            "WHERE source_document_id = ?", (document_id,),
        ).fetchone()[0]
    return document_id, str(global_id)


def test_repair_rejects_multiple_live_running_jobs_atomically(
    tmp_path: Path,
) -> None:
    database = tmp_path / "multiple-live.sqlite3"
    service = KnowledgeService(database)
    for index in range(2):
        job_id = _queued_project(service, f"live-{index}")
        _mark_running(database, job_id, "2999-01-01T00:00:00+00:00")
    before = _database_state(database)

    with pytest.raises(sqlite3.DatabaseError, match="multiple live RUNNING"):
        KnowledgeService(database)

    assert _database_state(database) == before


def test_repair_recovers_expired_job_alongside_one_live_job(
    tmp_path: Path,
) -> None:
    database = tmp_path / "expired-and-live.sqlite3"
    service = KnowledgeService(database)
    expired_id = _queued_project(service, "expired")
    live_id = _queued_project(service, "live")
    _mark_running(database, expired_id, "2000-01-01T00:00:00+00:00")
    _mark_running(database, live_id, "2999-01-01T00:00:00+00:00")

    reopened = KnowledgeService(database)

    assert reopened.job(expired_id)["state"] == "QUEUED"
    assert reopened.job(expired_id)["owner"] is None
    assert reopened.job(live_id)["state"] == "RUNNING"


def test_repair_rejects_unparseable_running_lease_atomically(
    tmp_path: Path,
) -> None:
    database = tmp_path / "invalid-lease.sqlite3"
    service = KnowledgeService(database)
    job_id = _queued_project(service, "invalid-lease")
    _mark_running(database, job_id, "not-a-timestamp")
    before = _database_state(database)

    with pytest.raises(sqlite3.DatabaseError, match="invalid RUNNING job lease"):
        KnowledgeService(database)

    assert _database_state(database) == before


def test_linked_global_mirror_rejects_public_writes_without_side_effects(
    tmp_path: Path,
) -> None:
    service = KnowledgeService(tmp_path / "mirror-guard.sqlite3")
    source_id, global_id = _linked_mirror(service)
    operations: tuple[Callable[[], None], ...] = (
        lambda: service.append_state(global_id, "verified"),
        lambda: service.edit_candidate(global_id, "tampered", "test"),
        lambda: service.soft_delete_candidate(global_id),
    )

    for operation in operations:
        before = _knowledge_state(service._database)
        jobs_before = service.count("jobs")
        with pytest.raises(RuntimeError, match="system-managed global mirror"):
            operation()
        assert _knowledge_state(service._database) == before
        assert service.count("jobs") == jobs_before + 1
        assert service.job(jobs_before + 1)["state"] == "FAILED"

    service.record_feedback(source_id, str(uuid.uuid4()), True)
    assert service.job(service.count("jobs"))["state"] == "SUCCEEDED"
    assert service.document_versions(global_id)[-1]["state"] == "verified"


def test_unlinked_global_documents_keep_public_mutation_behavior(
    tmp_path: Path,
) -> None:
    service = KnowledgeService(tmp_path / "independent-global.sqlite3")
    project_id, document_id = _candidate(
        service, "independent-global", scope="global"
    )
    service.edit_candidate(document_id, "revised", "test")
    service.append_state(document_id, "approved")
    _, deleted_id = _candidate(
        service, "independent-global-delete", scope="global"
    )
    service.soft_delete_candidate(deleted_id)

    assert [
        row["state"] for row in service.document_versions(document_id)
    ] == ["candidate", "candidate", "approved"]
    with sqlite3.connect(service._database) as connection:
        deleted_at = connection.execute(
            "SELECT deleted_at FROM documents WHERE id = ?", (deleted_id,),
        ).fetchone()[0]
    assert deleted_at is not None
    assert service.project(project_id)["scope"] == "global"


def test_schema_three_public_mutations_do_not_require_global_sync(
    tmp_path: Path,
) -> None:
    service = KnowledgeService(tmp_path / "schema-three.sqlite3")
    service.migrate(3)
    _, document_id = _candidate(service, "schema-three")

    service.edit_candidate(document_id, "revised", "test")
    service.append_state(document_id, "approved")

    assert service.schema_version() == 3
    assert [
        row["state"] for row in service.document_versions(document_id)
    ] == ["candidate", "candidate", "approved"]
    with sqlite3.connect(service._database) as connection:
        table = connection.execute(
            "SELECT 1 FROM sqlite_master WHERE type = 'table' "
            "AND name = 'global_sync'"
        ).fetchone()
    assert table is None
