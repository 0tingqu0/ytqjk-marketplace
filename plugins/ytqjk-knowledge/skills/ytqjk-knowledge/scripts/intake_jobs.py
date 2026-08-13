import hashlib
import json
import uuid
from collections.abc import Callable
from dataclasses import replace
from datetime import datetime, timedelta, timezone
from typing import Any, Protocol

from .intake_contracts import IntakeJob, JobState


_STAGES = ("validate", "scan", "parse", "normalize", "candidate-write")
_ERROR_CATEGORIES = frozenset({
    "INTERNAL", "PARSER_FAILED", "SECURITY_FAILED", "TRANSIENT",
    "VALIDATION_FAILED", "WORKER_EXPIRED",
})


class IntakeJobRepository(Protocol):
    """Persistence port for intake job snapshots."""

    def add(self, job: IntakeJob) -> None:
        """Add new job snapshot."""

    def get(self, job_id: str) -> IntakeJob:
        """Return job snapshot by ID."""

    def save(self, job: IntakeJob) -> None:
        """Replace existing job snapshot."""

    def list(self) -> tuple[IntakeJob, ...]:
        """Return deterministic job snapshots."""


class InMemoryIntakeJobRepository:
    """Deterministic in-memory implementation of job repository."""

    def __init__(self) -> None:
        self._jobs: dict[str, IntakeJob] = {}
        self._next_order = 1

    def add(self, job: IntakeJob) -> None:
        """Add copied job with deterministic creation order."""
        if job.id in self._jobs:
            raise ValueError("job already exists")
        stored = replace(
            job, payload=_copy_payload(job.payload),
            created_order=self._next_order,
        )
        self._jobs[job.id] = stored
        self._next_order += 1

    def get(self, job_id: str) -> IntakeJob:
        """Return defensive copy of job snapshot."""
        return _copy_job(self._jobs[job_id])

    def save(self, job: IntakeJob) -> None:
        """Save job after immutable-field validation."""
        current = self.get(job.id)
        immutable = ("project_id", "payload", "input_digest", "created_order")
        changed = any(
            getattr(current, name) != getattr(job, name)
            for name in immutable
        )
        if changed:
            raise ValueError("immutable job fields changed")
        self._jobs[job.id] = _copy_job(job)

    def list(self) -> tuple[IntakeJob, ...]:
        """Return defensive copies in creation order."""
        return tuple(_copy_job(job) for job in sorted(
            self._jobs.values(), key=lambda value: value.created_order
        ))


class IntakeJobStateMachine:
    """Coordinate intake job lifecycle with lease fencing."""

    def __init__(
        self,
        repository: IntakeJobRepository,
        *,
        max_attempts: int = 3,
        lease_seconds: int = 30,
        clock: Callable[[], datetime] | None = None,
    ) -> None:
        if max_attempts <= 0 or lease_seconds <= 0:
            raise ValueError("retry and lease limits must be positive")
        methods = ("add", "get", "save", "list")
        missing = any(
            not callable(getattr(repository, name, None)) for name in methods
        )
        if missing:
            raise TypeError("repository must implement IntakeJobRepository")
        self._repository = repository
        self._max_attempts = max_attempts
        self._lease = timedelta(seconds=lease_seconds)
        self._clock = clock or (lambda: datetime.now(timezone.utc))

    def enqueue(self, project_id: str, payload: dict[str, Any]) -> IntakeJob:
        """Queue immutable payload for canonical project UUID."""
        project = _canonical_uuid(project_id, "project_id")
        encoded = _encoded_payload(payload)
        job = IntakeJob(
            id=str(uuid.uuid4()), project_id=project, state=JobState.QUEUED,
            payload=_copy_payload(payload),
            input_digest=hashlib.sha256(encoded.encode("utf-8")).hexdigest(),
            progress=0, stage="queued", attempt=0, owner=None, error=None,
        )
        self._repository.add(job)
        return self.get(job.id)

    def claim(
        self, owner: str, rescan: Callable[[dict[str, Any]], None] | None = None
    ) -> IntakeJob | None:
        """Claim next queued job and fence worker attempt."""
        worker = _text(owner, "worker owner")
        now = self._now()
        self._recover_expired(now)
        jobs = self._repository.list()
        eligible = (
            job
            for job in jobs
            if job.state is JobState.QUEUED
            and job.attempt < self._max_attempts
        )
        queued = next(eligible, None)
        if queued is None:
            return None
        if queued.stage == "retry" and rescan is None:
            self._security_fail(queued, "rescan required")
            return None
        if rescan is not None:
            try:
                rescan(_copy_payload(queued.payload))
            except Exception as error:
                self._security_fail(queued, type(error).__name__)
                return None
        claimed = replace(
            queued,
            state=JobState.RUNNING,
            owner=worker,
            attempt=queued.attempt + 1,
            stage="validate",
            lease_expires_at=now + self._lease,
            error=None, error_category=None,
        )
        self._repository.save(claimed)
        return self.get(claimed.id)

    def progress(
        self, job_id: str, owner: str, attempt: int, progress: int, stage: str
    ) -> IntakeJob:
        """Advance running job one stage with monotonic progress."""
        if not 0 <= progress < 100:
            raise ValueError("RUNNING progress must be between 0 and 99")
        target = _text(stage, "stage")
        if target not in _STAGES:
            raise ValueError("stage is invalid")
        current = self._running(job_id, owner, attempt)
        current_index = _STAGES.index(current.stage)
        target_index = _STAGES.index(target)
        if target_index != current_index + 1:
            raise ValueError("stage must advance exactly one step")
        if progress < current.progress:
            raise ValueError("progress cannot decrease")
        updated = replace(
            current, progress=progress, stage=target,
            lease_expires_at=self._now() + self._lease,
        )
        self._repository.save(updated)
        return self.get(job_id)

    def succeed(self, job_id: str, owner: str, attempt: int) -> IntakeJob:
        """Complete candidate-written job at 100 percent."""
        current = self._running(job_id, owner, attempt)
        if current.stage != "candidate-write":
            raise ValueError("job must reach candidate-write before success")
        updated = replace(
            current, state=JobState.SUCCEEDED, progress=100, stage="complete",
            owner=None, lease_expires_at=None,
        )
        self._repository.save(updated)
        return self.get(job_id)

    def fail(
        self, job_id: str, owner: str, attempt: int, category: str,
        summary: str, *, retry: bool = False,
    ) -> IntakeJob:
        """Fail job using safe category and digest summary."""
        current = self._running(job_id, owner, attempt)
        normalized = _error_category(category)
        should_retry = (
            retry
            and normalized != "SECURITY_FAILED"
            and current.attempt < self._max_attempts
        )
        updated = replace(
            current, state=JobState.QUEUED if should_retry else JobState.FAILED,
            stage="retry" if should_retry else "failed", owner=None,
            lease_expires_at=None, error=_safe_error(normalized, summary),
            error_category=normalized,
        )
        self._repository.save(updated)
        return self.get(job_id)

    def cancel(self, job_id: str) -> IntakeJob:
        """Cancel queued or running job."""
        current = self.get(job_id)
        if current.state not in {JobState.QUEUED, JobState.RUNNING}:
            raise ValueError("invalid cancellation transition")
        cancelled = replace(
            current, state=JobState.CANCELLED, stage="cancelled",
            owner=None, lease_expires_at=None,
        )
        self._repository.save(cancelled)
        return self.get(job_id)

    def retry(self, job_id: str) -> IntakeJob:
        """Requeue retryable failed job within attempt limit."""
        current = self.get(job_id)
        if current.state is not JobState.FAILED:
            raise ValueError("invalid retry transition")
        if current.attempt >= self._max_attempts:
            raise ValueError("retry limit reached")
        if current.error_category == "SECURITY_FAILED":
            raise ValueError("security failure cannot be retried")
        queued = replace(
            current, state=JobState.QUEUED, stage="retry", error=None,
            error_category=None,
        )
        self._repository.save(queued)
        return self.get(job_id)

    def get(self, job_id: str) -> IntakeJob:
        """Return job after immutable payload digest validation."""
        job = self._repository.get(job_id)
        digest = hashlib.sha256(
            _encoded_payload(job.payload).encode("utf-8")
        ).hexdigest()
        if digest != job.input_digest:
            raise ValueError("immutable job digest mismatch")
        return job

    def _running(self, job_id: str, owner: str, attempt: int) -> IntakeJob:
        current = self.get(job_id)
        if current.state is JobState.CANCELLED:
            raise ValueError("job cancelled")
        if current.state is not JobState.RUNNING:
            raise ValueError("invalid job transition")
        lease_lost = (
            current.owner != owner
            or current.attempt != attempt
            or current.lease_expires_at is None
            or current.lease_expires_at <= self._now()
        )
        if lease_lost:
            raise ValueError("job lease lost")
        return current

    def _recover_expired(self, now: datetime) -> None:
        for job in self._repository.list():
            expired = (
                job.state is JobState.RUNNING
                and job.lease_expires_at is not None
                and job.lease_expires_at <= now
            )
            if expired:
                retry = job.attempt < self._max_attempts
                self._repository.save(replace(
                    job, state=JobState.QUEUED if retry else JobState.FAILED,
                    stage="retry" if retry else "failed", owner=None,
                    lease_expires_at=None,
                    error=_safe_error("WORKER_EXPIRED", "lease expired"),
                    error_category="WORKER_EXPIRED",
                ))

    def _security_fail(self, job: IntakeJob, summary: str) -> None:
        self._repository.save(replace(
            job, state=JobState.FAILED, stage="security_failed", owner=None,
            lease_expires_at=None,
            error=_safe_error("SECURITY_FAILED", summary),
            error_category="SECURITY_FAILED",
        ))

    def _now(self) -> datetime:
        current = self._clock()
        if current.tzinfo is None:
            raise ValueError("clock must return timezone-aware datetime")
        return current.astimezone(timezone.utc)


IntakeJobStore = IntakeJobStateMachine


def _copy_job(job: IntakeJob) -> IntakeJob:
    return replace(job, payload=_copy_payload(job.payload))


def _copy_payload(payload: dict[str, Any]) -> dict[str, Any]:
    return json.loads(_encoded_payload(payload))


def _encoded_payload(payload: object) -> str:
    if not isinstance(payload, dict):
        raise ValueError("job payload must be an object")
    try:
        return json.dumps(
            payload,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        )
    except (TypeError, ValueError) as error:
        raise ValueError("job payload must be deterministic JSON") from error


def _text(value: str, name: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{name} is required")
    return value.strip()


def _canonical_uuid(value: str, name: str) -> str:
    text = _text(value, name)
    try:
        canonical = str(uuid.UUID(text))
    except ValueError as error:
        raise ValueError(f"{name} must be canonical UUID") from error
    if text != canonical:
        raise ValueError(f"{name} must be canonical UUID")
    return canonical


def _error_category(value: str) -> str:
    category = _text(value, "error category").upper()
    if category not in _ERROR_CATEGORIES:
        raise ValueError("error category is invalid")
    return category


def _safe_error(category: str, summary: str) -> str:
    safe_summary = _text(summary, "failure summary")
    reference = hashlib.sha256(
        safe_summary.encode("utf-8")
    ).hexdigest()[:16]
    return f"{category}:ref={reference}"
