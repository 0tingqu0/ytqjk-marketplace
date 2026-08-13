from __future__ import annotations

import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.intake_jobs import (  # noqa: E402
    InMemoryIntakeJobRepository,
    IntakeJobStateMachine,
    IntakeJobStore,
    JobState,
)


PROJECT_ID = "00000000-0000-0000-0000-000000000001"


def _store(**kwargs) -> IntakeJobStateMachine:
    return IntakeJobStateMachine(InMemoryIntakeJobRepository(), **kwargs)


def _advance_to_candidate_write(
    store: IntakeJobStateMachine, job_id: str, owner: str, attempt: int
) -> None:
    stages = (
        (20, "scan"),
        (40, "parse"),
        (60, "normalize"),
        (90, "candidate-write"),
    )
    for progress, stage in stages:
        store.progress(job_id, owner, attempt, progress, stage)


def test_job_lifecycle_progress_and_cancel(tmp_path: Path) -> None:
    store = _store(max_attempts=2)
    job = store.enqueue(PROJECT_ID, {"source": "note.txt"})
    running = store.claim("worker-a")
    assert running is not None and running.id == job.id
    assert running.state is JobState.RUNNING and running.attempt == 1
    store.progress(job.id, "worker-a", running.attempt, 20, "scan")
    store.cancel(job.id)
    with pytest.raises(ValueError, match="cancelled"):
        store.succeed(job.id, "worker-a", running.attempt)
    assert store.get(job.id).state is JobState.CANCELLED


def test_invalid_transition_and_progress_fail_closed(tmp_path: Path) -> None:
    store = _store()
    job = store.enqueue(PROJECT_ID, {})
    with pytest.raises(ValueError, match="transition"):
        store.succeed(job.id, "worker-a", 0)
    running = store.claim("worker-a")
    assert running is not None
    with pytest.raises(ValueError, match="progress"):
        store.progress(job.id, "worker-a", running.attempt, 101, "bad")
    with pytest.raises(ValueError, match="stage"):
        store.progress(job.id, "worker-a", running.attempt, 50, "")
    with pytest.raises(ValueError, match="exactly one"):
        store.progress(job.id, "worker-a", running.attempt, 50, "parse")
    store.progress(job.id, "worker-a", running.attempt, 20, "scan")
    with pytest.raises(ValueError, match="decrease"):
        store.progress(job.id, "worker-a", running.attempt, 19, "parse")
    with pytest.raises(ValueError, match="RUNNING"):
        store.progress(job.id, "worker-a", running.attempt, 100, "complete")
    with pytest.raises(ValueError, match="stage"):
        store.progress(job.id, "worker-a", running.attempt, 60, "upload")


def test_expired_worker_is_fenced_and_job_retried(tmp_path: Path) -> None:
    clock = [datetime(2026, 1, 1, tzinfo=timezone.utc)]
    store = _store(
        max_attempts=2,
        lease_seconds=10,
        clock=lambda: clock[0],
    )
    job = store.enqueue(PROJECT_ID, {})
    first = store.claim("worker-a")
    assert first is not None
    clock[0] += timedelta(seconds=11)
    second = store.claim("worker-b", rescan=lambda _: None)
    assert second is not None and second.attempt == 2
    with pytest.raises(ValueError, match="lease lost"):
        store.fail(
            job.id,
            "worker-a",
            first.attempt,
            "TRANSIENT",
            "late failure",
            retry=True,
        )
    _advance_to_candidate_write(store, job.id, "worker-b", second.attempt)
    store.succeed(job.id, "worker-b", second.attempt)
    assert store.get(job.id).progress == 100


def test_retry_limit_and_explicit_retry_policy(tmp_path: Path) -> None:
    store = _store(max_attempts=2)
    job = store.enqueue(PROJECT_ID, {})
    first = store.claim("worker")
    assert first is not None
    store.fail(
        job.id,
        "worker",
        first.attempt,
        "TRANSIENT",
        "temporary",
        retry=True,
    )
    second = store.claim("worker", rescan=lambda _: None)
    assert second is not None and second.attempt == 2
    store.fail(
        job.id,
        "worker",
        second.attempt,
        "TRANSIENT",
        "again",
        retry=True,
    )
    assert store.get(job.id).state is JobState.FAILED
    with pytest.raises(ValueError, match="retry limit"):
        store.retry(job.id)


def test_cancel_queued_and_missing_job(tmp_path: Path) -> None:
    store = _store()
    with pytest.raises((AttributeError, TypeError)):
        IntakeJobStore(tmp_path / "intake.sqlite3")  # type: ignore[arg-type]
    with pytest.raises(ValueError, match="payload"):
        store.enqueue(PROJECT_ID, [])  # type: ignore[arg-type]
    with pytest.raises(ValueError, match="UUID"):
        store.enqueue("project-a", {})
    job = store.enqueue(PROJECT_ID, {})
    assert store.cancel(job.id).state is JobState.CANCELLED
    with pytest.raises(KeyError):
        store.get("missing")


def test_retry_keeps_digest_and_security_failure_is_not_retried(
    tmp_path: Path,
) -> None:
    store = _store()
    job = store.enqueue(PROJECT_ID, {"source_sha256": "a" * 64})
    digest = job.input_digest
    first = store.claim("worker")
    assert first is not None
    store.fail(
        job.id,
        "worker",
        first.attempt,
        "TRANSIENT",
        "temporary",
        retry=True,
    )
    assert store.get(job.id).input_digest == digest

    def reject(_: dict[str, object]) -> None:
        raise ValueError("security blocked")

    assert store.claim("worker", rescan=reject) is None
    failed = store.get(job.id)
    assert failed.state is JobState.FAILED
    assert failed.stage == "security_failed"
    with pytest.raises(ValueError, match="security failure"):
        store.retry(job.id)


def test_failure_storage_redacts_secret_path_and_exception(
    tmp_path: Path,
) -> None:
    store = _store()
    job = store.enqueue(PROJECT_ID, {})
    running = store.claim("worker")
    assert running is not None
    attack = (
        "token=super-secret C:\\Users\\victim\\secret.txt "
        "RuntimeError(raw)"
    )
    failed = store.fail(
        job.id, "worker", running.attempt, "PARSER_FAILED", attack
    )
    assert failed.error is not None
    assert failed.error.startswith("PARSER_FAILED:ref=")
    assert "secret" not in failed.error
    assert "Users" not in failed.error
    assert "RuntimeError" not in failed.error


def test_success_requires_candidate_write_stage(tmp_path: Path) -> None:
    store = _store()
    job = store.enqueue(PROJECT_ID, {})
    running = store.claim("worker")
    assert running is not None
    with pytest.raises(ValueError, match="candidate-write"):
        store.succeed(job.id, "worker", running.attempt)
    _advance_to_candidate_write(store, job.id, "worker", running.attempt)
    assert store.succeed(job.id, "worker", running.attempt).progress == 100
