"""Durable worker for structured image and PDF candidate intake."""

from __future__ import annotations

import hashlib
import json
import threading
import uuid
from dataclasses import asdict, is_dataclass
from enum import Enum
from pathlib import Path
from typing import Callable, Mapping

from knowledge_engine_locator import EngineNotConfigured, EnginePlanner
from knowledge_engine_locator import EngineProcessingError, KnowledgeEngine
from knowledge_engine_locator import legacy_report
from rag_security import contains_high_confidence_secret
from structured_candidate_writer import (
    StructuredCandidateWriteError,
    cleanup_artifacts,
    write_structured_candidate,
)


def safe_runtime_root(root: Path) -> Path:
    base = root.resolve() / ".runtime" / "document-intake"
    if _is_link(base) or _is_link(base.parent):
        raise RuntimeError("linked intake runtime is blocked")
    base.mkdir(parents=True, exist_ok=True)
    resolved = base.resolve(strict=True)
    resolved.relative_to(root.resolve())
    return resolved


def safe_output(root: Path, path: Path) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    if _is_link(path) or _is_link(path.parent):
        raise RuntimeError("linked intake output is blocked")
    resolved = path.resolve()
    resolved.relative_to(root.resolve())
    return resolved


def _is_link(path: Path) -> bool:
    return path.is_symlink() or getattr(path, "is_junction", lambda: False)()


def analyze_legacy_intake(
    name: str, content: str, source_bytes: int,
) -> dict[str, object]:
    lines = [line.strip() for line in content.splitlines() if line.strip()]
    title = next((line.lstrip("#").strip() for line in lines
                  if line.startswith("#")), Path(name).stem)
    return {
        "format": Path(name).suffix.lower().lstrip("."),
        "bytes": source_bytes, "lines": len(content.splitlines()),
        "title": title, "summary": next(
            (line for line in lines if not line.startswith("---")), ""
        )[:240],
    }


def validate_legacy_purpose(purpose: str) -> str:
    normalized = purpose.strip()
    if len(normalized) > 500 or "\x00" in normalized \
            or contains_high_confidence_secret(normalized):
        raise ValueError("资料作用无效或可能包含敏感内容。")
    return normalized


def validate_legacy_relative_path(relative_path: str) -> str:
    normalized = relative_path.replace("\\", "/").strip("/")
    if normalized and (
        len(normalized) > 500 or "\x00" in normalized
        or any(part in {"", ".", ".."} for part in normalized.split("/"))
    ):
        raise ValueError("文件夹位置无效。")
    return normalized


class IntakeProcessingError(RuntimeError):
    def __init__(self, category: str, code: str) -> None:
        super().__init__(code)
        self.category = category
        self.code = code


class DocumentIntakeWorker:
    def __init__(
        self, root: Path, engine: KnowledgeEngine, store: object,
        planner: Callable[[bytes, str, str, str], object] | None = None,
    ) -> None:
        self.root = root.resolve()
        self.engine = engine
        self.store = store
        self.planner = planner or EnginePlanner(self.root, engine)
        self.owner = f"dashboard-{uuid.uuid4()}"
        self._thread: threading.Thread | None = None
        self._lock = threading.Lock()

    def kick(self) -> None:
        with self._lock:
            if self._thread is not None and self._thread.is_alive():
                return
            self._thread = threading.Thread(target=self._drain, daemon=True)
            self._thread.start()

    def process_one(self) -> bool:
        job = self.store.claim(self.owner)
        if job is None:
            return False
        try:
            self._run(job)
        except self.engine.module("document_intake_jobs").LeaseLostError:
            return True
        except EngineNotConfigured as error:
            self._failed(job, "TRANSIENT", str(error), "NOT_CONFIGURED", True)
        except (IntakeProcessingError, EngineProcessingError) as error:
            self._failed(job, error.category, error.code, error.code, True)
        except Exception as error:
            code = f"INTERNAL_{type(error).__name__.upper()}"
            self._failed(job, "INTERNAL", code, "INTERNAL", True)
        return True

    def _drain(self) -> None:
        while self.process_one():
            pass

    def _advance(self, current: object, stage: str) -> object:
        if current.stage != stage:
            return current
        return self.store.advance(
            current.id, self.owner, current.attempt, stage)

    def _run(self, job: object) -> None:
        source = self._source(job)
        name = str(job.config["source_name"])
        purpose = str(job.config["purpose"])
        media = str(job.config["media_type"])
        current = job
        current = self._advance(current, "validate")
        self._security(source)
        current = self._advance(current, "security-scan")
        plan = self.planner(source, name, purpose, media)
        value = _candidate_value(plan)
        pages = len(value["metadata"]["pages"])
        if current.stage == "page-detect":
            current = self.store.advance(
                current.id, self.owner, current.attempt, "page-detect",
                page_count=pages)
        elif current.page_count != pages:
            raise IntakeProcessingError("INTERNAL", "PAGE_COUNT_CHANGED")
        while current.stage not in {"candidate-write", "complete"}:
            current = self.store.advance(
                current.id, self.owner, current.attempt, current.stage)
        try:
            written = write_structured_candidate(
                self.root, current.id, value, source, name,
            )
        except StructuredCandidateWriteError as error:
            raise IntakeProcessingError(
                "WRITE_FAILED", error.code,
            ) from error
        result, created = written.value, written.created
        result_path, result_created = self._write_result(
            current.id, current.attempt, {
                "status": "CANDIDATE", "retryable": False, **result,
            })
        created += (result_path,) if result_created else ()
        try:
            if current.stage == "candidate-write":
                current = self._advance(current, "candidate-write")
            self.store.complete(current.id, self.owner, current.attempt)
        except Exception:
            cleanup_artifacts(created)
            raise
        release_staging(self.root, self.store, current)

    def _source(self, job: object) -> bytes:
        raw_path = self.root / str(job.payload["staging_ref"])
        path = raw_path.resolve()
        path.relative_to(self.root)
        if not path.is_file() or raw_path.is_symlink():
            raise IntakeProcessingError("VALIDATION_FAILED", "STAGING_MISSING")
        source = path.read_bytes()
        if hashlib.sha256(source).hexdigest() != job.payload["source_sha256"]:
            raise IntakeProcessingError("VALIDATION_FAILED", "DIGEST_MISMATCH")
        return source

    def _security(self, source: bytes) -> None:
        scanner = self.engine.module("intake_security").LocalScanner()
        result = scanner.scan(source, "source")
        if result.state.value != "CLEAN":
            raise IntakeProcessingError(
                "SECURITY_FAILED", "SOURCE_SECRET_BLOCKED")

    def _write_result(
        self, job_id: str, attempt: int, value: dict[str, object],
    ) -> tuple[Path, bool]:
        path = self.root / ".runtime" / "document-intake" / "results" \
            / f"{uuid.UUID(job_id)}.json"
        path = safe_output(self.root, path)
        content = _json_bytes({**value, "attempt": attempt})
        if path.exists() and path.read_bytes() != content:
            try:
                existing = json.loads(path.read_text(encoding="utf-8"))
            except (OSError, UnicodeError, json.JSONDecodeError) as error:
                raise IntakeProcessingError(
                    "WRITE_FAILED", "RESULT_CONFLICT") from error
            if not isinstance(existing, dict) or existing.get(
                "attempt", attempt
            ) >= attempt:
                raise IntakeProcessingError(
                    "WRITE_FAILED", "RESULT_CONFLICT")
            _atomic(path, content)
            return path, False
        return path, _write_once(path, content)

    def _failed(
        self, job: object, category: str, detail: str,
        status: str, retryable: bool,
    ) -> None:
        try:
            current = self.store.get(job.id)
            self._write_result(current.id, current.attempt, {
                "status": status, "retryable": retryable})
            self.store.fail(
                current.id, self.owner, current.attempt, category, detail)
        except self.engine.module("document_intake_jobs").LeaseLostError:
            return


def _candidate_value(plan: object) -> dict[str, object]:
    state = getattr(getattr(plan, "state", None), "value", None)
    if not is_dataclass(plan) or state != "CANDIDATE":
        raise IntakeProcessingError("INTERNAL", "INVALID_CANDIDATE_PLAN")

    def encode(value: object) -> object:
        if isinstance(value, Enum):
            return value.value
        if is_dataclass(value):
            value = asdict(value)
        if isinstance(value, Mapping):
            return {str(key): encode(item) for key, item in value.items()}
        if isinstance(value, (tuple, list)):
            return [encode(item) for item in value]
        return value

    result = encode(plan)
    if isinstance(result, dict):
        return result
    raise IntakeProcessingError("INTERNAL", "INVALID_CANDIDATE_PLAN")


def _json_bytes(value: object) -> bytes:
    return (json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True) + "\n").encode("utf-8")


def _atomic(path: Path, content: bytes) -> None:
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    try:
        temporary.write_bytes(content)
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)


def _write_once(path: Path, content: bytes) -> bool:
    if path.exists():
        if path.read_bytes() != content:
            raise IntakeProcessingError("WRITE_FAILED", "CANDIDATE_CONFLICT")
        return False
    _atomic(path, content)
    return True


def release_staging(root: Path, store: object, job: object) -> bool:
    jobs = store.list(limit=1_000)
    shared = any(
        item.id != job.id and item.payload == job.payload
        and item.state.value in {"QUEUED", "RUNNING", "FAILED"}
        for item in jobs)
    if len(jobs) >= 1_000 or shared:
        return False
    raw = root.resolve() / str(job.payload["staging_ref"])
    path = raw.resolve()
    try:
        path.relative_to((safe_runtime_root(root) / "staging").resolve())
    except ValueError:
        return False
    if not path.is_file() or raw.is_symlink():
        return False
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    if digest != job.payload["source_sha256"]:
        return False
    path.unlink()
    return True
