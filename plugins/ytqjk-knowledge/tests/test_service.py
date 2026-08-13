from __future__ import annotations

import json
import sqlite3
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.migrations import migrate  # noqa: E402
from scripts.database import transaction  # noqa: E402
from scripts.service import KnowledgeService  # noqa: E402


@contextmanager
def raw_connection(database: Path):
    """Open and explicitly close test-only raw DB connection."""
    current = sqlite3.connect(database)
    try:
        yield current
    finally:
        current.close()


@pytest.fixture
def service(tmp_path: Path) -> KnowledgeService:
    return KnowledgeService(tmp_path / "knowledge.sqlite3")


def candidate(service: KnowledgeService, alias: str = "project") -> tuple[str, str]:
    project_id = service.create_project("project", alias)
    document_id = service.create_candidate(project_id, "note", "draft", "test")
    return project_id, document_id


def test_migration_upgrade_rollback_and_concurrent_idempotency(tmp_path: Path) -> None:
    database = tmp_path / "knowledge.sqlite3"
    with ThreadPoolExecutor(max_workers=2) as pool:
        versions = list(pool.map(lambda _: KnowledgeService(database).schema_version(), range(2)))
    assert versions == [2, 2]
    first = KnowledgeService(database)
    first.migrate(1)
    assert first.schema_version() == 1
    project_id = first.create_project("project", "v1-writes")
    document_id = first.create_candidate(project_id, "v1", "queue works", "test")
    assert first.document_versions(document_id)[0]["state"] == "candidate"
    assert first.job(first.count("jobs"))["state"] == "SUCCEEDED"
    first.migrate(2)
    assert first.schema_version() == 2
    snapshot_id = first.create_snapshot(project_id)
    assert first.active_snapshot(project_id)["id"] == snapshot_id
    with ThreadPoolExecutor(max_workers=2) as pool:
        list(pool.map(lambda _: KnowledgeService(database).schema_version(), range(2)))
    assert KnowledgeService(database).schema_version() == 2


def test_failed_ddl_migration_is_atomic(tmp_path: Path) -> None:
    database = tmp_path / "broken-migration.sqlite3"
    with raw_connection(database) as current:
        current.execute("CREATE TABLE originals (id TEXT PRIMARY KEY)")
        with pytest.raises(sqlite3.OperationalError, match="already exists"):
            migrate(current, 1)
        tables = current.execute("SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'projects'").fetchall()
    assert tables == []


def test_project_uuid_scope_and_alias_are_immutable(service: KnowledgeService) -> None:
    project_id = service.create_project("project", "agentic")
    assert service.create_project("project", "agentic") == project_id
    with raw_connection(service._database) as current:
        with pytest.raises(sqlite3.IntegrityError, match="immutable"):
            current.execute("UPDATE projects SET alias = 'changed' WHERE id = ?", (project_id,))


def test_project_dedupe_key_uses_unambiguous_canonical_json(
    service: KnowledgeService,
) -> None:
    first = service.create_project("a:b", "c")
    second = service.create_project("a", "b:c")
    assert first != second
    assert service.count("jobs") == 2


def test_content_addressing_and_append_only_storage(service: KnowledgeService) -> None:
    project_id = service.create_project("project", "dedupe")
    service.create_candidate(project_id, "one", "same content", "test")
    service.create_candidate(project_id, "two", "same content", "test")
    assert service.count("originals") == 1
    with raw_connection(service._database) as current:
        with pytest.raises(sqlite3.IntegrityError, match="originals are immutable"):
            current.execute("UPDATE originals SET content = x'78'")
        with pytest.raises(sqlite3.IntegrityError, match="originals are immutable"):
            current.execute("DELETE FROM originals")
        with pytest.raises(sqlite3.IntegrityError, match="audit is append-only"):
            current.execute("DELETE FROM audit")
        with pytest.raises(sqlite3.IntegrityError, match="audit is append-only"):
            current.execute("UPDATE audit SET detail = 'changed'")


def test_candidate_and_terminal_governance_state_machine(service: KnowledgeService) -> None:
    _, document_id = candidate(service, "states")
    service.edit_candidate(document_id, "revised", "test")
    service.append_state(document_id, "approved")
    with pytest.raises(RuntimeError, match="only active candidate"):
        service.edit_candidate(document_id, "blocked", "test")
    service.append_state(document_id, "verified")
    service.append_state(document_id, "tombstone")
    with pytest.raises(RuntimeError, match="invalid governance"):
        service.append_state(document_id, "approved")
    with raw_connection(service._database) as current:
        latest = current.execute("SELECT original_sha256 FROM versions WHERE document_id = ? ORDER BY ordinal DESC LIMIT 1", (document_id,)).fetchone()
        with pytest.raises(sqlite3.IntegrityError, match="invalid governance"):
            current.execute("INSERT INTO versions(document_id, ordinal, state, original_sha256, created_at) VALUES (?, 6, 'approved', ?, '2026-01-01T00:00:00+00:00')", (document_id, latest[0]))
        with pytest.raises(sqlite3.IntegrityError, match="only active candidates"):
            current.execute("UPDATE documents SET deleted_at = '2026-01-01T00:00:00+00:00' WHERE id = ?", (document_id,))
    assert [row["state"] for row in service.document_versions(document_id)] == [
        "candidate", "candidate", "approved", "verified", "tombstone"
    ]


def test_invalid_public_payload_never_creates_job(service: KnowledgeService) -> None:
    assert not hasattr(service, "enqueue_job")
    assert not hasattr(service, "run_next")
    with pytest.raises(ValueError, match="identifier must be UUID"):
        service.create_candidate("not-uuid", "note", "draft", "test")
    assert service.count("jobs") == 0


def test_injected_job_payload_revalidates_before_handler(service: KnowledgeService) -> None:
    with raw_connection(service._database) as current:
        with pytest.raises(sqlite3.IntegrityError, match="jobs must begin queued"):
            current.execute("INSERT INTO jobs(kind, payload, state, created_at) VALUES ('create_project', '{}', 'SUCCEEDED', '2026-01-01T00:00:00+00:00')")
        payload = {
            "document_id": "00000000-0000-0000-0000-000000000010",
            "project_id": "00000000-0000-0000-0000-000000000011",
            "title": "valid shape",
            "content": "must roll back",
            "source": "test",
        }
        current.execute("INSERT INTO jobs(kind, payload, state, created_at) VALUES ('create_candidate', ?, 'QUEUED', '2026-01-01T00:00:00+00:00')", (json.dumps(payload),))
        job_id = int(current.execute("SELECT last_insert_rowid()").fetchone()[0])
        current.commit()
    service.create_project("project", "after-injection")
    assert service.job(job_id)["state"] == "FAILED"
    assert service.count("documents") == 0
    assert service.count("originals") == 0
    assert service.count("versions") == 0


def test_concurrent_deduplication_uses_one_fifo_writer(service: KnowledgeService) -> None:
    project_id = service.create_project("project", "concurrent")

    def create(_: int) -> str:
        return service.create_candidate(project_id, "same", "same content", "test")

    with ThreadPoolExecutor(max_workers=8) as pool:
        document_ids = list(pool.map(create, range(16)))
    assert len(set(document_ids)) == 1
    assert service.count("documents") == 1
    assert {service.job(index)["state"] for index in range(1, service.count("jobs") + 1)} == {"SUCCEEDED"}


def test_multiple_service_instances_remain_single_fifo_writer(tmp_path: Path) -> None:
    database = tmp_path / "shared.sqlite3"
    first, second = KnowledgeService(database), KnowledgeService(database)
    project_id = first.create_project("project", "shared")
    with ThreadPoolExecutor(max_workers=2) as pool:
        document_ids = list(pool.map(
            lambda pair: pair[0].create_candidate(project_id, pair[1], pair[1], "test"),
            ((first, "one"), (second, "two")),
        ))
    assert len(set(document_ids)) == 2
    assert first.count("documents") == 2


def test_expired_running_job_is_recovered_before_fifo_claim(service: KnowledgeService) -> None:
    first_payload = {"id": "00000000-0000-0000-0000-000000000001", "scope": "project", "alias": "first"}
    second_payload = {"id": "00000000-0000-0000-0000-000000000002", "scope": "project", "alias": "second"}
    with raw_connection(service._database) as current:
        current.execute("INSERT INTO jobs(kind, payload, state, created_at) VALUES ('create_project', ?, 'QUEUED', '2000-01-01T00:00:00+00:00')", (json.dumps(first_payload),))
        first = int(current.execute("SELECT last_insert_rowid()").fetchone()[0])
        current.execute("INSERT INTO jobs(kind, payload, state, created_at) VALUES ('create_project', ?, 'QUEUED', '2000-01-01T00:00:00+00:00')", (json.dumps(second_payload),))
        second = int(current.execute("SELECT last_insert_rowid()").fetchone()[0])
        current.execute("UPDATE jobs SET state = 'RUNNING', owner = 'dead', heartbeat_at = '2000-01-01T00:00:00+00:00', lease_expires_at = '2000-01-01T00:00:00+00:00', attempt = 1 WHERE id = ?", (first,))
        current.commit()
    service.create_project("project", "third")
    assert service.job(first)["state"] == "SUCCEEDED"
    assert service.job(second)["state"] == "SUCCEEDED"
    assert service.job(first)["attempt"] == 2


def test_expired_attempt_cannot_heartbeat_or_finish_after_reclaim(
    service: KnowledgeService,
) -> None:
    payload = {
        "id": "00000000-0000-0000-0000-000000000021",
        "scope": "project",
        "alias": "fenced",
    }
    job_id = service._queue.submit("create_project", payload)
    first_claim = service._queue._claim()
    assert first_claim is not None
    first_attempt = int(first_claim["attempt"])
    with raw_connection(service._database) as current:
        current.execute(
            "UPDATE jobs SET heartbeat_at = ?, lease_expires_at = ? WHERE id = ?",
            ("2000-01-01T00:00:00+00:00", "2000-01-01T00:00:00+00:00", job_id),
        )
        current.commit()
    second_claim = service._queue._claim()
    assert second_claim is not None
    assert int(second_claim["attempt"]) == first_attempt + 1
    with transaction(service._database) as current:
        with pytest.raises(RuntimeError, match="job lease lost"):
            service._queue._heartbeat(current, job_id, first_attempt)
        with pytest.raises(RuntimeError, match="job lease lost"):
            service._queue._finish(current, job_id, first_attempt, "SUCCEEDED", None)


def test_failed_job_rolls_back_domain_write(service: KnowledgeService) -> None:
    project_id = service.create_project("project", "rollback")
    with raw_connection(service._database) as current:
        current.execute("UPDATE projects SET name = ? WHERE id = ?", ("allowed", project_id))
        current.commit()
    with pytest.raises(ValueError, match="text field"):
        service.create_candidate(project_id, "note", "", "test")
    assert service.count("documents") == 0
    assert service.count("originals") == 0
    assert service.count("versions") == 0


def test_snapshot_generations_and_membership_are_immutable(service: KnowledgeService) -> None:
    project_id, document_id = candidate(service, "snapshots")
    first = service.create_snapshot(project_id)
    first_read = service.read_active_snapshot(project_id)
    service.edit_candidate(document_id, "v2", "test")
    second = service.create_snapshot(project_id)
    second_read = service.read_active_snapshot(project_id)
    assert first_read is not None and second_read is not None
    assert first_read["snapshot"]["id"] == first
    assert second_read["snapshot"]["id"] == second
    assert first_read["versions"] != second_read["versions"]
    with raw_connection(service._database) as current:
        with pytest.raises(sqlite3.IntegrityError, match="snapshots are immutable"):
            current.execute("DELETE FROM snapshots WHERE id = ?", (first,))
        with pytest.raises(sqlite3.IntegrityError, match="snapshot membership"):
            current.execute("DELETE FROM snapshot_versions WHERE snapshot_id = ?", (first,))
        with pytest.raises(sqlite3.IntegrityError, match="snapshot membership"):
            current.execute("UPDATE snapshot_versions SET version_id = ? WHERE snapshot_id = ?", (second_read["versions"][0]["version_id"], first))
        with pytest.raises(sqlite3.IntegrityError, match="snapshot membership requires"):
            current.execute("INSERT INTO snapshot_versions VALUES (?, ?, ?)", (first, document_id, second_read["versions"][0]["version_id"]))


def test_closed_connections_release_windows_database_file(tmp_path: Path) -> None:
    database = tmp_path / "removable.sqlite3"
    KnowledgeService(database).create_project("project", "remove")
    database.unlink()
    assert not database.exists()


def test_skill_documented_import_works_in_isolated_python() -> None:
    code = "import sys; sys.path.insert(0, sys.argv[1]); from scripts.service import KnowledgeService"
    result = subprocess.run(
        [sys.executable, "-I", "-c", code, str(SKILL_ROOT)],
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode == 0, result.stderr
