from __future__ import annotations

import json
import sqlite3
import sys
import uuid
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
TRIGGER_SQL = "SELECT name FROM sqlite_master WHERE type='trigger'"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.feedback_migration import _TABLES  # noqa: E402
from scripts.service import KnowledgeService  # noqa: E402


@pytest.fixture
def service(tmp_path: Path) -> KnowledgeService:
    return KnowledgeService(tmp_path / "feedback.sqlite3")


def _candidate(service: KnowledgeService, alias: str = "feedback") -> tuple[str, str]:
    project_id = service.create_project("project", alias)
    document_id = service.create_candidate(project_id, "knowledge", "content", "test")
    return project_id, document_id


def _feedback(service: KnowledgeService, document_id: str, correct: bool) -> str:
    invocation_id = str(uuid.uuid4())
    service.record_feedback(document_id, invocation_id, correct)
    return invocation_id


def _states(service: KnowledgeService, document_id: str) -> list[str]:
    return [row["state"] for row in service.document_versions(document_id)]


def _global_id(service: KnowledgeService, document_id: str) -> str:
    with sqlite3.connect(service._database) as current:
        row = current.execute(
            "SELECT global_document_id FROM global_sync "
            "WHERE source_document_id = ?",
            (document_id,),
        ).fetchone()
    assert row is not None
    return str(row[0])


def _latest_content(service: KnowledgeService, document_id: str) -> str:
    with sqlite3.connect(service._database) as current:
        row = current.execute(
            "SELECT o.content FROM versions v JOIN originals o ON "
            "o.sha256 = v.original_sha256 WHERE v.document_id = ? "
            "ORDER BY v.ordinal DESC LIMIT 1",
            (document_id,),
        ).fetchone()
    assert row is not None
    return bytes(row[0]).decode("utf-8")


def _triggers(service: KnowledgeService) -> set[str]:
    with sqlite3.connect(service._database) as current:
        return {str(row[0]) for row in current.execute(TRIGGER_SQL)}


def test_three_correct_feedback_events_promote_and_sync(
    service: KnowledgeService,
) -> None:
    _, document_id = _candidate(service)

    first = _feedback(service, document_id, True)
    assert _states(service, document_id) == ["candidate"]
    status = service.feedback_status(document_id)
    assert (status["invocation_id"], status["correct"], status["score"]) == (
        first, 1, 1,
    )
    assert service.count("global_sync") == 0

    _feedback(service, document_id, True)
    global_id = _global_id(service, document_id)
    assert _states(service, document_id) == ["candidate", "approved"]
    assert _states(service, global_id) == ["candidate", "approved"]
    assert _latest_content(service, global_id) == "content"

    _feedback(service, document_id, True)
    assert _states(service, document_id)[-1] == "verified"
    assert _states(service, global_id)[-1] == "verified"


def test_incorrect_feedback_downgrades_and_recycles_with_content_sync(
    service: KnowledgeService,
) -> None:
    project_id, document_id = _candidate(service, "downgrade")
    for _ in range(3):
        _feedback(service, document_id, True)
    global_id = _global_id(service, document_id)

    _feedback(service, document_id, False)
    _feedback(service, document_id, False)
    assert (_states(service, document_id)[-1], _states(service, global_id)[-1]) == (
        "candidate",
        "candidate",
    )
    service.edit_candidate(document_id, "corrected content", "test")
    assert service.recycle_bin(project_id) == []

    _feedback(service, document_id, False)
    assert _states(service, document_id)[-1] == "tombstone"
    assert _states(service, global_id)[-1] == "tombstone"
    assert _latest_content(service, global_id) == "corrected content"
    assert [row["id"] for row in service.recycle_bin(project_id)] == [document_id]
    assert _states(service, global_id)[-3:] == [
        "candidate", "candidate", "tombstone",
    ]


def test_candidate_error_enters_recycle_bin_without_global_copy(
    service: KnowledgeService,
) -> None:
    project_id, document_id = _candidate(service, "direct-recycle")
    service.create_candidate(project_id, "active", "active", "test")
    _feedback(service, document_id, False)
    assert _states(service, document_id) == ["candidate", "tombstone"]
    assert service.count("global_sync") == 0
    assert [row["id"] for row in service.recycle_bin(project_id)] == [document_id]


def test_invocation_dedupe_conflict_and_canonical_validation(
    service: KnowledgeService,
) -> None:
    _, document_id = _candidate(service, "idempotent")
    invocation_id = str(uuid.uuid4())
    service.record_feedback(document_id, invocation_id, True)
    jobs_after_first = service.count("jobs")
    service.record_feedback(document_id, invocation_id, True)
    assert service.count("jobs") == jobs_after_first
    assert service.count("feedback_events") == 1

    with pytest.raises(RuntimeError, match="conflicts with existing event"):
        service.record_feedback(document_id, invocation_id, False)
    assert service.count("feedback_events") == 1
    assert service.count("jobs") == jobs_after_first + 1
    with sqlite3.connect(service._database) as current:
        failed_job_id = current.execute("SELECT MAX(id) FROM jobs").fetchone()[0]
    failed = service.job(failed_job_id)
    assert failed["state"] == "FAILED"
    assert json.loads(failed["payload"])["correct"] is False

    with pytest.raises(ValueError, match="canonical UUID"):
        service.record_feedback(document_id, invocation_id.upper(), True)
    invalid_correct: bool = 1  # type: ignore[assignment]
    with pytest.raises(ValueError, match="boolean"):
        service.record_feedback(document_id, str(uuid.uuid4()), invalid_correct)


def test_multiple_services_deduplicate_same_invocation(tmp_path: Path) -> None:
    database = tmp_path / "concurrent.sqlite3"
    first = KnowledgeService(database)
    second = KnowledgeService(database)
    _, document_id = _candidate(first, "concurrent-feedback")
    invocation_id = str(uuid.uuid4())

    with ThreadPoolExecutor(max_workers=2) as pool:
        futures = [
            pool.submit(item.record_feedback, document_id, invocation_id, True)
            for item in (first, second)
        ]
        assert [future.result() for future in futures] == [None, None]

    assert first.count("feedback_events") == 1
    assert first.feedback_status(document_id)["score"] == 1


def test_public_downgrade_remains_blocked_and_global_feedback_fails(
    service: KnowledgeService,
) -> None:
    _, document_id = _candidate(service, "boundaries")
    _feedback(service, document_id, True)
    service.append_state(document_id, "verified")
    version_count = len(service.document_versions(document_id))
    _feedback(service, document_id, True)
    global_id = _global_id(service, document_id)
    assert service.feedback_status(document_id)["score"] == 3
    assert service.count("feedback_events") == 2
    assert len(service.document_versions(document_id)) == version_count
    with pytest.raises(RuntimeError, match="invalid governance"):
        service.append_state(document_id, "approved")
    with pytest.raises(RuntimeError, match="must target a project"):
        _feedback(service, global_id, True)
    assert _states(service, document_id)[-1] == "verified"


def test_feedback_and_sync_records_are_append_only(
    service: KnowledgeService,
) -> None:
    _, document_id = _candidate(service, "append-only")
    _feedback(service, document_id, True)
    _feedback(service, document_id, True)
    with sqlite3.connect(service._database) as current:
        with pytest.raises(sqlite3.IntegrityError, match="append-only"):
            current.execute("UPDATE feedback_events SET correct = 0")
        with pytest.raises(sqlite3.IntegrityError, match="append-only"):
            current.execute("DELETE FROM feedback_events")
        with pytest.raises(sqlite3.IntegrityError, match="immutable"):
            current.execute("UPDATE global_sync SET created_at = 'changed'")
        with pytest.raises(sqlite3.IntegrityError, match="immutable"):
            current.execute("DELETE FROM global_sync")


def test_empty_migration_round_trip_and_schema_three_rejects_feedback(
    service: KnowledgeService,
) -> None:
    service.migrate(3)
    assert service.schema_version() == 3
    with pytest.raises(RuntimeError, match="requires schema v4"):
        service.record_feedback(str(uuid.uuid4()), str(uuid.uuid4()), True)
    service.migrate(4)
    assert service.schema_version() == 4


def test_nonempty_v4_downgrade_is_atomic_and_preserves_guards(
    service: KnowledgeService,
) -> None:
    _, document_id = _candidate(service, "downgrade-guard")
    _feedback(service, document_id, True)
    with pytest.raises(sqlite3.DatabaseError, match="feedback jobs"):
        service.migrate(3)

    with sqlite3.connect(service._database) as current:
        version = current.execute("PRAGMA user_version").fetchone()[0]
        event_count = current.execute(
            "SELECT COUNT(*) FROM feedback_events"
        ).fetchone()[0]
    assert (version, event_count) == (4, 1)
    assert {"feedback_events_immutable_delete", "versions_state_machine"} <= (
        _triggers(service)
    )


def test_feedback_jobs_block_downgrade_and_are_preserved(
    service: KnowledgeService,
) -> None:
    missing_id = str(uuid.uuid4())
    with pytest.raises(RuntimeError, match="knowledge document is unavailable"):
        service.record_feedback(missing_id, str(uuid.uuid4()), True)
    payload = {
        "document_id": missing_id,
        "invocation_id": str(uuid.uuid4()),
        "correct": False,
    }
    queued_id = service._queue.submit("record_feedback", payload)
    failed_id = queued_id - 1
    before = (service.job(failed_id), service.job(queued_id))

    with pytest.raises(sqlite3.DatabaseError, match="feedback jobs"):
        service.migrate(3)

    assert service.schema_version() == 4
    assert [service.job(item["id"])["state"] for item in before] == [
        "FAILED", "QUEUED",
    ]
    assert service.count("feedback_events") == 0
    assert service.count("global_sync") == 0
    assert {
        "feedback_events_immutable_delete",
        "jobs_insert_guard",
        "versions_state_machine",
    } <= _triggers(service)


def test_repair_restores_missing_feedback_and_queue_guards(
    service: KnowledgeService,
) -> None:
    database = service._database
    with sqlite3.connect(database) as current:
        current.execute("DROP TRIGGER feedback_events_immutable_delete")
        current.execute("DROP TRIGGER jobs_insert_guard")
        current.execute("DROP TRIGGER versions_state_machine")
        current.commit()

    KnowledgeService(database)
    with sqlite3.connect(database) as current:
        with pytest.raises(sqlite3.IntegrityError, match="jobs must begin queued"):
            current.execute(
                "INSERT INTO jobs(kind, payload, state, created_at) "
                "VALUES ('record_feedback', '{}', 'SUCCEEDED', 'now')"
            )
    assert {
        "feedback_events_immutable_delete",
        "jobs_insert_guard",
        "versions_state_machine",
    } <= _triggers(service)
    with sqlite3.connect(database) as current:
        current.execute(
            "CREATE TRIGGER forged_feedback BEFORE INSERT ON feedback_events "
            "BEGIN SELECT RAISE(IGNORE); END"
        )
        current.commit()
    with pytest.raises(sqlite3.DatabaseError, match="unexpected schema v4 trigger"):
        KnowledgeService(database)


@pytest.mark.parametrize(
    "definition",
    (
        _TABLES["feedback_events"].replace(
            "invocation_id TEXT NOT NULL", "invocation_id TEXT"
        ),
        _TABLES["feedback_events"].replace(
            "UNIQUE(document_id, invocation_id)", "CHECK(1)"
        ),
        _TABLES["feedback_events"].replace(" REFERENCES documents(id)", ""),
        _TABLES["feedback_events"].replace("CHECK(correct IN (0, 1))", ""),
    ),
)
def test_repair_rejects_incompatible_feedback_table(
    tmp_path: Path, definition: str
) -> None:
    database = tmp_path / "broken-schema.sqlite3"
    KnowledgeService(database)
    with sqlite3.connect(database) as current:
        current.execute("DROP TABLE feedback_events")
        current.execute(definition)
        current.commit()
    with pytest.raises(sqlite3.DatabaseError, match="schema v4 feedback_events"):
        KnowledgeService(database)


@pytest.mark.parametrize(
    "case",
    (
        ("not-a-uuid", 1, "candidate", "invocation UUID"),
        (None, 3, "verified", "feedback trajectory"),
    ),
)
def test_repair_rejects_bad_rows_atomically(
    service: KnowledgeService,
    case: tuple[str | None, int, str, str],
) -> None:
    invocation_id, score, state, message = case
    _, document_id = _candidate(service, "repair-rows")
    valid_id = _feedback(service, document_id, True)
    with sqlite3.connect(service._database) as current:
        current.execute("DROP TRIGGER feedback_events_immutable_update")
        current.execute(
            "UPDATE feedback_events SET invocation_id = ?, score = ?, state = ?",
            (invocation_id or valid_id, score, state),
        )
        current.commit()
    with pytest.raises(sqlite3.DatabaseError, match=message):
        KnowledgeService(service._database)
    assert service.schema_version() == 4
    assert service.count("feedback_events") == 1
    assert "feedback_events_immutable_update" not in _triggers(service)
