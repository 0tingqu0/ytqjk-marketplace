from __future__ import annotations

import sqlite3
import sys
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.document_intake_job_store import (  # noqa: E402
    DocumentIntakeJobStore,
)
from scripts.document_intake_jobs import (  # noqa: E402
    SCHEMA_VERSION,
    STAGES,
    JobState,
    JobTransitionError,
    JobValidationError,
    LeaseLostError,
)


DIGEST_A = "a" * 64


def payload(name: str = "document.pdf") -> dict[str, str]:
    return {
        "staging_ref": f"staging/{name}",
        "source_sha256": DIGEST_A,
    }


def test_full_persistent_lifecycle_uses_all_real_stages(
    tmp_path: Path,
) -> None:
    database = tmp_path / "intake.sqlite3"
    store = DocumentIntakeJobStore(database)
    queued = store.enqueue(payload(), {"ocr": {"language": "zh"}})
    assert queued.state is JobState.QUEUED
    assert queued.stage == STAGES[0]
    running = store.claim("worker-a")
    assert running is not None and running.attempt == 1
    progress = running.progress
    seen = [running.stage]
    for stage in STAGES[:-1]:
        kwargs = {"page_count": 4} if stage == "page-detect" else {}
        running = store.advance(
            running.id, "worker-a", running.attempt, stage, **kwargs
        )
        assert running.progress >= progress
        progress = running.progress
        seen.append(running.stage)
        if running.stage == "ocr-primary":
            revision = running.revision
            running = store.heartbeat(
                running.id, "worker-a", running.attempt
            )
            assert running.revision == revision + 1
    assert tuple(seen) == STAGES
    assert running.progress == 99 and running.page_count == 4
    finished = store.complete(running.id, "worker-a", running.attempt)
    assert finished.state is JobState.SUCCEEDED
    assert finished.progress == 100 and finished.owner is None
    reopened = DocumentIntakeJobStore(database)
    assert reopened.get(finished.id) == finished
    assert reopened.schema_version() == SCHEMA_VERSION


def test_progress_depends_on_detected_pages_not_fixed_milestones(
    tmp_path: Path,
) -> None:
    store = DocumentIntakeJobStore(tmp_path / "progress.sqlite3")
    first = store.enqueue(payload("one.pdf"))
    second = store.enqueue(payload("ten.pdf"), {"profile": "other"})
    one = store.claim("one")
    ten = store.claim("ten")
    assert one is not None and one.id == first.id
    assert ten is not None and ten.id == second.id
    for stage in ("validate", "security-scan"):
        one = store.advance(one.id, "one", one.attempt, stage)
        ten = store.advance(ten.id, "ten", ten.attempt, stage)
    one = store.advance(
        one.id, "one", one.attempt, "page-detect", page_count=1
    )
    ten = store.advance(
        ten.id, "ten", ten.attempt, "page-detect", page_count=10
    )
    assert one.progress > ten.progress > 0
    assert one.progress not in {20, 45, 75}


def test_same_input_and_config_is_transactionally_idempotent(
    tmp_path: Path,
) -> None:
    store = DocumentIntakeJobStore(tmp_path / "dedupe.sqlite3")
    first = store.enqueue(payload(), {"b": 2, "a": 1})
    reversed_payload = dict(reversed(tuple(payload().items())))
    same = store.enqueue(reversed_payload, {"a": 1, "b": 2})
    different = store.enqueue(payload(), {"a": 2, "b": 2})
    assert first.id == same.id
    assert first.idempotency_key == same.idempotency_key
    assert different.id != first.id
    assert len(store.list()) == 2


def test_concurrent_double_claim_has_one_winner(tmp_path: Path) -> None:
    database = tmp_path / "claim.sqlite3"
    first_store = DocumentIntakeJobStore(database)
    first_store.enqueue(payload())
    second_store = DocumentIntakeJobStore(database)
    stores = (first_store, second_store)
    with ThreadPoolExecutor(max_workers=2) as pool:
        results = list(pool.map(
            lambda pair: pair[1].claim(f"worker-{pair[0]}"),
            enumerate(stores),
        ))
    winners = [job for job in results if job is not None]
    assert len(winners) == 1
    assert winners[0].attempt == 1


def test_restart_recovers_only_expired_running_and_fences_old_attempt(
    tmp_path: Path,
) -> None:
    clock = [100.0]
    database = tmp_path / "recovery.sqlite3"
    store = DocumentIntakeJobStore(
        database, lease_seconds=10, clock=lambda: clock[0]
    )
    job = store.enqueue(payload())
    first = store.claim("worker-a")
    assert first is not None
    with pytest.raises(LeaseLostError):
        store.heartbeat(job.id, "worker-a", True)
    clock[0] = 105
    live = store.heartbeat(job.id, "worker-a", first.attempt)
    assert live.lease_expires_at == 115
    clock[0] = 111
    restarted = DocumentIntakeJobStore(
        database, lease_seconds=10, clock=lambda: clock[0]
    )
    assert restarted.get(job.id).state is JobState.RUNNING
    clock[0] = 116
    assert restarted.recover_expired() == 1
    assert restarted.get(job.id).state is JobState.QUEUED
    second = restarted.claim("worker-b")
    assert second is not None and second.attempt == 2
    with pytest.raises(LeaseLostError, match="lease lost"):
        restarted.heartbeat(job.id, "worker-a", first.attempt)


def test_fail_retry_cancel_and_safe_error_reference(tmp_path: Path) -> None:
    database = tmp_path / "failure.sqlite3"
    store = DocumentIntakeJobStore(database)
    job = store.enqueue(payload())
    running = store.claim("worker")
    assert running is not None
    raw = "token=secret C:\\Users\\victim\\document.pdf RuntimeError(raw)"
    failed = store.fail(
        job.id, "worker", running.attempt, "OCR_FAILED", raw
    )
    assert failed.state is JobState.FAILED
    assert failed.error_category == "OCR_FAILED"
    assert len(failed.error_ref or "") == 64
    with sqlite3.connect(database) as current:
        stored = " ".join(str(value) for value in current.execute(
            "SELECT error_category,error_ref FROM document_intake_jobs"
        ).fetchone())
    assert "secret" not in stored and "Users" not in stored
    queued = store.retry(job.id)
    assert queued.state is JobState.QUEUED
    second = store.claim("worker")
    assert second is not None and second.attempt == 2
    cancelled = store.cancel(job.id)
    assert cancelled.state is JobState.CANCELLED
    with pytest.raises(LeaseLostError):
        store.complete(job.id, "worker", second.attempt)


def test_security_failure_and_attempt_limit_cannot_retry(
    tmp_path: Path,
) -> None:
    security = DocumentIntakeJobStore(tmp_path / "security.sqlite3")
    job = security.enqueue(payload())
    running = security.claim("worker")
    assert running is not None
    security.fail(
        job.id, "worker", running.attempt,
        "SECURITY_FAILED", "malware signature",
    )
    with pytest.raises(JobTransitionError, match="not retryable"):
        security.retry(job.id)
    limited = DocumentIntakeJobStore(
        tmp_path / "limited.sqlite3", max_attempts=1
    )
    job = limited.enqueue(payload())
    running = limited.claim("worker")
    assert running is not None
    limited.fail(job.id, "worker", running.attempt, "TRANSIENT", "busy")
    with pytest.raises(JobTransitionError, match="limit"):
        limited.retry(job.id)


@pytest.mark.parametrize(
    "bad_payload",
    [
        {"staging_ref": "C:/secret.pdf", "source_sha256": DIGEST_A},
        {"staging_ref": "../secret.pdf", "source_sha256": DIGEST_A},
        {"staging_ref": "staging/x.pdf", "source_sha256": b"bytes"},
        {
            "staging_ref": "staging/x.pdf",
            "source_sha256": DIGEST_A,
            "raw_bytes": "forbidden",
        },
    ],
)
def test_payload_allows_only_relative_staging_ref_and_digests(
    tmp_path: Path, bad_payload: dict[str, object]
) -> None:
    store = DocumentIntakeJobStore(tmp_path / "strict.sqlite3")
    with pytest.raises(JobValidationError):
        store.enqueue(bad_payload)
    with pytest.raises(JobValidationError, match="secret"):
        store.enqueue(payload(), {"api_token": "forbidden"})
    with pytest.raises(JobValidationError, match="strict JSON"):
        store.enqueue(payload(), {"bad": (1, 2)})
    with pytest.raises(JobValidationError, match="config"):
        store.enqueue(payload(), [])  # type: ignore[arg-type]
    with pytest.raises(JobValidationError, match="number"):
        store.enqueue(payload(), {"pages": 10**10_000})


def test_store_options_are_strict(tmp_path: Path) -> None:
    database = tmp_path / "options.sqlite3"
    with pytest.raises(JobValidationError, match="options"):
        DocumentIntakeJobStore(database, lease_seconds=True)
    with pytest.raises(JobValidationError, match="options"):
        DocumentIntakeJobStore(
            database, max_attempts=1.5  # type: ignore[arg-type]
        )
    with pytest.raises(JobValidationError, match="options"):
        DocumentIntakeJobStore(database, clock=0)  # type: ignore[arg-type]


def test_invalid_advance_rolls_back_and_revision_fk_is_enforced(
    tmp_path: Path,
) -> None:
    database = tmp_path / "atomic.sqlite3"
    store = DocumentIntakeJobStore(database)
    queued = store.enqueue(payload())
    running = store.claim("worker")
    assert running is not None
    revision = running.revision
    with pytest.raises(JobTransitionError):
        store.advance(
            running.id, "worker", running.attempt, "security-scan"
        )
    assert store.get(running.id).revision == revision
    with sqlite3.connect(database) as current:
        current.execute("PRAGMA foreign_keys=ON")
        with pytest.raises(sqlite3.IntegrityError):
            current.execute(
                "INSERT INTO document_intake_job_revisions "
                "VALUES ('missing',0,'x','QUEUED','validate',0,0)"
            )
        count = current.execute(
            "SELECT COUNT(*) FROM document_intake_job_revisions "
            "WHERE job_id=?", (queued.id,),
        ).fetchone()[0]
    assert count == revision + 1
