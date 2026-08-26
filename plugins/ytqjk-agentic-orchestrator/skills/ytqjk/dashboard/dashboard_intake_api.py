"""HTTP-neutral durable intake API used by the local dashboard."""

from __future__ import annotations

import base64
import binascii
import hashlib
import json
import re
import uuid
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath, PureWindowsPath

from approval_assessment import assess_for_approval
from dashboard_intake_worker import (
    DocumentIntakeWorker,
    analyze_legacy_intake,
    legacy_report,
    release_staging, safe_runtime_root,
    validate_legacy_purpose,
    validate_legacy_relative_path,
)
from intake_formats import (
    SUPPORTED_EXTENSIONS,
    TEXT_EXTENSIONS,
    TEXT_FILE_NAMES,
    extract_upload,
    supported_extension,
)
from knowledge_chunks import write_chunks
from knowledge_dedup import find_duplicate
from knowledge_engine_locator import locate_knowledge_engine
from rag_security import contains_high_confidence_secret, is_sensitive_path


STRUCTURED_EXTENSIONS = frozenset({
    ".bmp", ".gif", ".jpeg", ".jpg", ".pdf", ".png", ".tif", ".tiff",
    ".webp",
})
_JOB_ROUTE = re.compile(
    r"^/api/intake/jobs/([0-9a-f-]{36})(?:/(retry|cancel))?$"
)
_SAFE_FILE_NAME = re.compile(
    r"[\w .()（）-]+\.[A-Za-z0-9]+", re.UNICODE
)
_INTAKE_DIR = "personal-experience/candidates/imports"


def intake_document(
    root: Path, name: str, content: str, purpose: str = "",
) -> dict[str, object]:
    return intake_upload(root, name, content.encode("utf-8"), purpose)


def intake_upload(
    root: Path,
    name: str,
    source: bytes,
    purpose: str = "",
    relative_path: str = "",
) -> dict[str, object]:
    source_name = Path(name).name.strip()
    named = _SAFE_FILE_NAME.fullmatch(source_name) is not None
    if (
        not source_name or source_name != name
        or (not named and source_name.lower() not in TEXT_FILE_NAMES)
        or supported_extension(source_name) not in SUPPORTED_EXTENSIONS
    ):
        raise ValueError("仅支持文本、Office 和常见媒体资料。")
    if is_sensitive_path(source_name):
        raise ValueError("资料可能包含凭据或敏感文件名，未保存。")
    if not source or len(source) > 10 * 1024 * 1024:
        raise ValueError("单份资料必须非空且不能超过 10 MiB。")
    content, details = extract_upload(source_name, source)
    normalized_purpose = validate_legacy_purpose(purpose)
    if "\x00" in content or contains_high_confidence_secret(content):
        raise ValueError("资料可能包含凭据或敏感内容，未保存。")
    extension = supported_extension(source_name)
    if extension in TEXT_EXTENSIONS and not content.strip():
        raise ValueError("文本资料必须非空。")
    duplicate = find_duplicate(root, content, source)
    if duplicate is not None:
        raise ValueError(f"发现相同知识，未重复入库：{duplicate}")
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    identifier = f"{timestamp}-{uuid.uuid4().hex[:8]}-{Path(name).stem}"
    target = root / _INTAKE_DIR / f"{identifier}.md"
    original = target.parent / "originals" / f"{identifier}-{source_name}"
    target.parent.mkdir(parents=True, exist_ok=True)
    original.parent.mkdir(parents=True, exist_ok=True)
    analysis = analyze_legacy_intake(source_name, content, len(source))
    assessment = assess_for_approval(content, "dimensions" in details)
    original_path = original.relative_to(root).as_posix()
    metadata = (
        "---\nstatus: CANDIDATE\nsource: dashboard-intake\n"
        f"intake_id: {identifier}\noriginal_name: {source_name}\n"
        f"original_path: {original_path}\n"
        f"received_at: {datetime.now(timezone.utc).isoformat()}\n---\n\n"
    )
    report = legacy_report(
        analysis, normalized_purpose,
        validate_legacy_relative_path(relative_path), details,
        original_path, assessment,
    )
    original.write_bytes(source)
    chunks = write_chunks(root, identifier, source_name, content)
    report = report.replace(
        "- 原件：", f"- 知识片段：{len(chunks)} 个\n- 原件："
    )
    target.write_text(
        metadata + report + (content or "图片未进行文字识别，已记录文件元数据。"), encoding="utf-8"
    )
    return {
        "path": target.relative_to(root).as_posix(),
        "state": "candidate",
        "assessment": assessment,
        "chunks": len(chunks),
    }


class IntakeApiError(ValueError):
    def __init__(self, status: int, message: str) -> None:
        super().__init__(message)
        self.status = status


class DashboardIntakeApi:
    def __init__(
        self,
        root: Path,
        plugin_root: Path,
        *,
        max_bytes: int = 10 * 1024 * 1024,
        worker: object | None = None,
        store: object | None = None,
    ) -> None:
        self.root = root.resolve()
        self.max_bytes = max_bytes
        engine = locate_knowledge_engine(plugin_root)
        runtime = safe_runtime_root(self.root)
        store_class = engine.module(
            "document_intake_job_store"
        ).DocumentIntakeJobStore
        self.store = store or store_class(
            runtime / "jobs.sqlite3", lease_seconds=180,
        )
        self.worker = worker or DocumentIntakeWorker(
            self.root, engine, self.store
        )
        self.scanner = engine.module("intake_security").LocalScanner()

    @staticmethod
    def handles(path: str) -> bool:
        return path == "/api/intake" or _JOB_ROUTE.fullmatch(path) is not None

    @staticmethod
    def structured_name(payload: object) -> bool:
        if not isinstance(payload, dict):
            return False
        name = payload.get("name")
        return (
            isinstance(name, str)
            and Path(name).suffix.casefold() in STRUCTURED_EXTENSIONS
        )

    def submit(self, payload: object) -> dict[str, object]:
        if not isinstance(payload, dict):
            raise IntakeApiError(400, "请求格式错误。")
        name = self._name(payload.get("name"))
        if Path(name).suffix.casefold() not in STRUCTURED_EXTENSIONS:
            raise IntakeApiError(400, "该格式不使用结构化入库队列。")
        if payload.get("encoding") != "base64":
            raise IntakeApiError(400, "图片和 PDF 必须使用 base64 投递。")
        raw = payload.get("content")
        if not isinstance(raw, str):
            raise IntakeApiError(400, "资料内容无效。")
        try:
            source = base64.b64decode(raw, validate=True)
        except (binascii.Error, ValueError) as error:
            raise IntakeApiError(400, "资料 base64 内容无效。") from error
        if not source or len(source) > self.max_bytes:
            raise IntakeApiError(413, "单份资料必须非空且不能超过 10 MiB。")
        scan = self.scanner.scan(source, "dashboard-staging")
        if scan.state.value != "CLEAN":
            raise IntakeApiError(400, "资料可能包含凭据或敏感内容。")
        digest = hashlib.sha256(source).hexdigest()
        suffix = Path(name).suffix.casefold()
        reference = f".runtime/document-intake/staging/{digest}{suffix}"
        self._stage(reference, source, digest)
        job = self.store.enqueue(
            {"staging_ref": reference, "source_sha256": digest},
            {
                "media_type": "pdf" if suffix == ".pdf" else "image",
                "purpose": self._purpose(payload.get("purpose", "")),
                "source_name": name,
            },
        )
        if job.state.value in {"SUCCEEDED", "CANCELLED"}:
            release_staging(self.root, self.store, job)
        self.worker.kick()
        return {"ok": True, "job": self._job(job)}

    def get(self, path: str) -> dict[str, object]:
        job_id, action = self._route(path)
        if action is not None:
            raise IntakeApiError(404, "API not found")
        try:
            return {"ok": True, "job": self._job(self.store.get(job_id))}
        except KeyError as error:
            raise IntakeApiError(404, "入库任务不存在。") from error

    def action(self, path: str) -> tuple[int, dict[str, object]]:
        job_id, action = self._route(path)
        if action is None:
            raise IntakeApiError(404, "API not found")
        try:
            if action == "retry":
                job = self.store.retry(job_id)
                self.worker.kick()
                return 202, {"ok": True, "job": self._job(job)}
            job = self.store.cancel(job_id)
            release_staging(self.root, self.store, job)
            return 200, {"ok": True, "job": self._job(job)}
        except KeyError as error:
            raise IntakeApiError(404, "入库任务不存在。") from error
        except ValueError as error:
            raise IntakeApiError(409, "当前任务状态不允许该操作。") from error

    def _job(self, job: object) -> dict[str, object]:
        value = {
            "id": job.id,
            "state": job.state.value,
            "stage": job.stage,
            "progress": job.progress,
            "page_count": job.page_count,
            "attempt": job.attempt,
            "revision": job.revision,
            "created_at": job.created_at,
            "updated_at": job.updated_at,
        }
        if job.error_category is not None:
            value["error"] = {
                "category": job.error_category,
                "ref": job.error_ref,
                "retryable": job.error_category not in {
                    "SECURITY_FAILED", "VALIDATION_FAILED",
                },
            }
        if job.state.value in {"FAILED", "SUCCEEDED"}:
            result = self._result(job.id)
            if result is not None:
                value["result"] = result
        return value

    def _result(self, job_id: str) -> dict[str, object] | None:
        path = self.root / ".runtime" / "document-intake" / "results" \
            / f"{uuid.UUID(job_id)}.json"
        path = path.resolve()
        if (
            self.root not in path.parents
            or not path.is_file() or path.is_symlink()
        ):
            return None
        try:
            if path.stat().st_size > 32 * 1024 * 1024:
                return None
            value = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, UnicodeError, json.JSONDecodeError):
            return None
        return value if isinstance(value, dict) else None

    def _stage(self, reference: str, source: bytes, digest: str) -> None:
        raw_path = self.root / reference
        path = raw_path.resolve()
        path.relative_to(self.root)
        path.parent.mkdir(parents=True, exist_ok=True)
        if path.exists():
            if raw_path.is_symlink() or hashlib.sha256(
                path.read_bytes()
            ).hexdigest() != digest:
                raise IntakeApiError(409, "暂存资料完整性冲突。")
            return
        temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
        try:
            temporary.write_bytes(source)
            temporary.replace(path)
        finally:
            temporary.unlink(missing_ok=True)

    @staticmethod
    def _name(value: object) -> str:
        if not isinstance(value, str) or not value.strip():
            raise IntakeApiError(400, "资料名称无效。")
        name = PurePosixPath(PureWindowsPath(value).name).name.strip()
        if name != value or name in {".", ".."} or is_sensitive_path(name):
            raise IntakeApiError(400, "资料名称无效或可能敏感。")
        if len(name) > 240 or any(ord(char) < 32 for char in name):
            raise IntakeApiError(400, "资料名称无效。")
        return name

    @staticmethod
    def _purpose(value: object) -> str:
        if not isinstance(value, str):
            raise IntakeApiError(400, "资料作用无效。")
        purpose = value.strip() or "供知识库人工复审"
        if len(purpose) > 500 or "\x00" in purpose:
            raise IntakeApiError(400, "资料作用无效。")
        return purpose

    @staticmethod
    def _route(path: str) -> tuple[str, str | None]:
        matched = _JOB_ROUTE.fullmatch(path)
        if matched is None:
            raise IntakeApiError(404, "API not found")
        try:
            job_id = str(uuid.UUID(matched.group(1)))
        except ValueError as error:
            raise IntakeApiError(404, "入库任务不存在。") from error
        return job_id, matched.group(2)


__all__ = [
    "DashboardIntakeApi",
    "IntakeApiError",
    "intake_document",
    "intake_upload",
]
