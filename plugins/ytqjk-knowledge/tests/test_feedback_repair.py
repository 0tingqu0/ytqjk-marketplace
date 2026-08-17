from __future__ import annotations

import json
import sqlite3
import sys
import uuid
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts import queue as queue_module  # noqa: E402
from scripts.feedback_domain import apply_feedback  # noqa: E402
from scripts.feedback_schema import TABLES  # noqa: E402
from scripts.service import KnowledgeService  # noqa: E402


def _candidate(
    service: KnowledgeService, alias: str, content: str = "content"
) -> tuple[str, str]:
    project_id = service.create_project("project", alias)
    document_id = service.create_candidate(
        project_id, "knowledge", content, "test"
    )
    return project_id, document_id


def _feedback(
    service: KnowledgeService, document_id: str, correct: bool
) -> str:
    invocation_id = str(uuid.uuid4())
    service.record_feedback(document_id, invocation_id, correct)
    return invocation_id


def _states(service: KnowledgeService, document_id: str) -> list[str]:
    return [row["state"] for row in service.document_versions(document_id)]


def _event_rows(service: KnowledgeService) -> list[tuple[object, ...]]:
    with sqlite3.connect(service._database) as connection:
        return connection.execute(
            "SELECT job_id, input_version_id, result_version_id, "
            "global_result_version_id FROM feedback_events ORDER BY id"
        ).fetchall()


def test_repair_uses_event_pk_when_all_timestamps_match(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    timestamp = "2026-08-17T00:00:00+00:00"
    monkeypatch.setattr(queue_module, "now", lambda: timestamp)
    database = tmp_path / "same-time.sqlite3"
    service = KnowledgeService(database)
    _, document_id = _candidate(service, "same-time")

    for _ in range(3):
        _feedback(service, document_id, True)

    reopened = KnowledgeService(database)
    assert _states(reopened, document_id) == [
        "candidate",
        "approved",
        "verified",
    ]
    assert reopened.feedback_status(document_id)["score"] == 3


def test_repair_rejects_false_candidate_event_with_score_one(
    tmp_path: Path,
) -> None:
    service = KnowledgeService(tmp_path / "false-candidate.sqlite3")
    _, document_id = _candidate(service, "false-candidate")
    _feedback(service, document_id, True)
    with sqlite3.connect(service._database) as connection:
        event = connection.execute(
            "SELECT job_id, invocation_id FROM feedback_events"
        ).fetchone()
        assert event is not None
        payload = {
            "document_id": document_id,
            "invocation_id": str(event[1]),
            "correct": False,
        }
        connection.execute("DROP TRIGGER feedback_events_immutable_update")
        connection.execute("DROP TRIGGER jobs_payload_immutable")
        connection.execute("UPDATE feedback_events SET correct = 0")
        connection.execute(
            "UPDATE jobs SET payload = ? WHERE id = ?",
            (json.dumps(payload, sort_keys=True, separators=(",", ":")), event[0]),
        )
        connection.commit()

    with pytest.raises(sqlite3.DatabaseError, match="feedback trajectory"):
        KnowledgeService(service._database)


def test_repair_rejects_event_without_job_and_bad_result_causality(
    tmp_path: Path,
) -> None:
    missing_job = KnowledgeService(tmp_path / "missing-job.sqlite3")
    _, document_id = _candidate(missing_job, "missing-job")
    _feedback(missing_job, document_id, True)
    with sqlite3.connect(missing_job._database) as connection:
        connection.execute("PRAGMA foreign_keys = OFF")
        connection.execute("DROP TRIGGER feedback_events_immutable_update")
        connection.execute("UPDATE feedback_events SET job_id = 999999")
        connection.commit()
    with pytest.raises(sqlite3.DatabaseError, match="orphaned schema v4 row"):
        KnowledgeService(missing_job._database)

    bad_result = KnowledgeService(tmp_path / "bad-result.sqlite3")
    _, first_id = _candidate(bad_result, "bad-result")
    _, second_id = _candidate(bad_result, "bad-result-second")
    _feedback(bad_result, first_id, True)
    other_version = bad_result.document_versions(second_id)[0]["id"]
    with sqlite3.connect(bad_result._database) as connection:
        connection.execute("DROP TRIGGER feedback_events_immutable_update")
        connection.execute(
            "UPDATE feedback_events SET result_version_id = ?", (other_version,)
        )
        connection.commit()
    with pytest.raises(sqlite3.DatabaseError, match="feedback version document"):
        KnowledgeService(bad_result._database)


def test_repair_rejects_non_succeeded_feedback_job(tmp_path: Path) -> None:
    service = KnowledgeService(tmp_path / "failed-job.sqlite3")
    _, document_id = _candidate(service, "failed-job")
    _feedback(service, document_id, True)
    with sqlite3.connect(service._database) as connection:
        connection.execute("DROP TRIGGER jobs_state_machine")
        connection.execute(
            "UPDATE jobs SET state = 'FAILED' WHERE kind = 'record_feedback'"
        )
        connection.commit()

    with pytest.raises(sqlite3.DatabaseError, match="feedback job state"):
        KnowledgeService(service._database)


def test_repair_rejects_orphan_succeeded_feedback_job(tmp_path: Path) -> None:
    service = KnowledgeService(tmp_path / "orphan-job.sqlite3")
    _, document_id = _candidate(service, "orphan-job")
    payload = {
        "document_id": document_id,
        "invocation_id": str(uuid.uuid4()),
        "correct": True,
    }
    job_id = service._queue.submit("record_feedback", payload)
    with sqlite3.connect(service._database) as connection:
        connection.execute("DROP TRIGGER jobs_state_machine")
        connection.execute(
            "UPDATE jobs SET state = 'SUCCEEDED', attempt = 1, owner = 'forged', "
            "started_at = 'now', finished_at = 'now' WHERE id = ?",
            (job_id,),
        )
        connection.commit()

    with pytest.raises(sqlite3.DatabaseError, match="orphaned feedback job"):
        KnowledgeService(service._database)


def test_first_error_downgrades_preexisting_approved_and_verified(
    tmp_path: Path,
) -> None:
    database = tmp_path / "legacy-states.sqlite3"
    service = KnowledgeService(database)
    _, approved_id = _candidate(service, "old-approved")
    _, verified_id = _candidate(service, "old-verified")
    service.append_state(approved_id, "approved")
    service.append_state(verified_id, "verified")

    _feedback(service, approved_id, False)
    _feedback(service, verified_id, False)

    assert _states(service, approved_id) == ["candidate", "approved", "candidate"]
    assert _states(service, verified_id) == ["candidate", "verified", "approved"]
    KnowledgeService(database)


def test_candidate_edit_between_events_preserves_feedback_high_water(
    tmp_path: Path,
) -> None:
    database = tmp_path / "candidate-edit.sqlite3"
    service = KnowledgeService(database)
    _, document_id = _candidate(service, "candidate-edit", "first")
    _feedback(service, document_id, True)
    service.edit_candidate(document_id, "second", "test")
    edited_version = service.document_versions(document_id)[-1]["id"]

    _feedback(service, document_id, True)

    events = _event_rows(service)
    assert events[1][1] == edited_version
    assert events[1][2] == service.document_versions(document_id)[-1]["id"]
    assert service.feedback_status(document_id)["score"] == 2
    KnowledgeService(database)


def test_repair_accepts_content_refresh_mirror_suffix(tmp_path: Path) -> None:
    database = tmp_path / "mirror-refresh.sqlite3"
    service = KnowledgeService(database)
    _, document_id = _candidate(service, "mirror-refresh", "first")
    for _ in range(3):
        _feedback(service, document_id, True)
    _feedback(service, document_id, False)
    _feedback(service, document_id, False)
    service.edit_candidate(document_id, "second", "test")
    _feedback(service, document_id, False)
    with sqlite3.connect(database) as connection:
        global_id = connection.execute(
            "SELECT global_document_id FROM global_sync"
        ).fetchone()[0]

    reopened = KnowledgeService(database)
    assert _states(reopened, global_id)[-3:] == [
        "candidate",
        "candidate",
        "tombstone",
    ]


def test_repair_rejects_forged_global_mirror_result(tmp_path: Path) -> None:
    service = KnowledgeService(tmp_path / "mirror.sqlite3")
    _, document_id = _candidate(service, "mirror")
    _feedback(service, document_id, True)
    _feedback(service, document_id, True)
    with sqlite3.connect(service._database) as connection:
        global_id = connection.execute(
            "SELECT global_document_id FROM global_sync"
        ).fetchone()[0]
        candidate_version = connection.execute(
            "SELECT id FROM versions WHERE document_id = ? ORDER BY ordinal LIMIT 1",
            (global_id,),
        ).fetchone()[0]
        connection.execute("DROP TRIGGER feedback_events_immutable_update")
        connection.execute(
            "UPDATE feedback_events SET global_result_version_id = ? "
            "WHERE global_result_version_id IS NOT NULL",
            (candidate_version,),
        )
        connection.commit()

    with pytest.raises(sqlite3.DatabaseError, match="global mirror suffix"):
        KnowledgeService(service._database)


@pytest.mark.parametrize(
    "old,new",
    (
        (
            "job_id INTEGER NOT NULL UNIQUE REFERENCES jobs(id)",
            "job_id INTEGER NOT NULL",
        ),
        (
            "input_version_id INTEGER NOT NULL REFERENCES versions(id)",
            "input_version_id INTEGER NOT NULL",
        ),
        (
            "global_result_version_id INTEGER REFERENCES versions(id)",
            "global_result_version_id INTEGER",
        ),
    ),
)
def test_repair_rejects_missing_feedback_causal_constraints(
    tmp_path: Path, old: str, new: str
) -> None:
    database = tmp_path / f"constraint-{uuid.uuid4()}.sqlite3"
    KnowledgeService(database)
    with sqlite3.connect(database) as connection:
        connection.execute("DROP TABLE feedback_events")
        connection.execute(TABLES["feedback_events"].replace(old, new))
        connection.commit()

    with pytest.raises(sqlite3.DatabaseError, match="schema v4 feedback_events"):
        KnowledgeService(database)


def test_domain_requires_one_matching_running_feedback_job(tmp_path: Path) -> None:
    service = KnowledgeService(tmp_path / "running-job.sqlite3")
    _, document_id = _candidate(service, "running-job")
    payload = {
        "document_id": document_id,
        "invocation_id": str(uuid.uuid4()),
        "correct": True,
    }
    with sqlite3.connect(service._database) as connection:
        with pytest.raises(RuntimeError, match="exactly one running feedback job"):
            apply_feedback(connection, payload, queue_module.now())

    service._queue.submit("record_feedback", payload)
    claimed = service._queue._claim()
    assert claimed is not None
    mismatched = dict(payload)
    mismatched["invocation_id"] = str(uuid.uuid4())
    with sqlite3.connect(service._database) as connection:
        with pytest.raises(RuntimeError, match="payload does not match"):
            apply_feedback(connection, mismatched, queue_module.now())
