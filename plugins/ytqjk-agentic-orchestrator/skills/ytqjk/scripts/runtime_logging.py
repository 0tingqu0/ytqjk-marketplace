"""Safe, rotating runtime logging for YTQJK long-running services."""

from __future__ import annotations

import logging
import os
import re
import secrets
import time
import traceback
from datetime import datetime, timezone
from logging.handlers import RotatingFileHandler
from pathlib import Path
from threading import Lock


LOGGER_NAME = "ytqjk"
LOG_LEVEL_ENV = "YTQJK_LOG_LEVEL"
DEFAULT_MAX_BYTES = 10 * 1024 * 1024
DEFAULT_BACKUP_COUNT = 5
_CONFIG_LOCK = Lock()
_MANAGED = "_ytqjk_managed_handler"
_ALLOWED_FIELDS = frozenset({
    "changed",
    "duration_ms",
    "error_type",
    "method",
    "peer_status",
    "port",
    "reason",
    "request_id",
    "route",
    "status",
})
_METHODS = frozenset({"DELETE", "GET", "HEAD", "OPTIONS", "POST", "PUT"})


class _EventFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        timestamp = datetime.fromtimestamp(
            record.created, timezone.utc
        ).isoformat(timespec="milliseconds").replace("+00:00", "Z")
        event = _safe_value(
            getattr(record, "ytqjk_event", record.getMessage())
        )
        parts = [
            timestamp,
            f"level={record.levelname}",
            f"logger={_safe_value(record.name)}",
            f"event={event}",
        ]
        fields = getattr(record, "ytqjk_fields", {})
        if isinstance(fields, dict):
            parts.extend(
                f"{key}={_safe_value(fields[key])}"
                for key in sorted(fields)
                if key in _ALLOWED_FIELDS
            )
        if record.exc_info:
            parts.append(self.formatException(record.exc_info))
        return " ".join(parts)

    def formatException(self, exc_info: tuple[object, object, object]) -> str:
        exception_type = getattr(exc_info[0], "__name__", "Exception")
        frames = traceback.extract_tb(exc_info[2])[-8:] if exc_info[2] else []
        trace = ">".join(
            f"{Path(frame.filename).name}:{frame.lineno}:{_safe_value(frame.name)}"
            for frame in frames
        )
        result = f"exception={_safe_value(exception_type)}"
        return f"{result} trace={trace}" if trace else result


class SafeHttpLogMixin:
    """Emit one safe request summary without using raw request lines."""

    request_log_component = "http"

    def handle_one_request(self) -> None:
        self._ytqjk_request_started = time.perf_counter()
        self._ytqjk_request_id = secrets.token_hex(8)
        try:
            super().handle_one_request()
        except Exception as error:
            started = getattr(
                self,
                "_ytqjk_request_started",
                time.perf_counter(),
            )
            log_exception(
                get_logger(self.request_log_component),
                "http_request_failed",
                error,
                method=self._request_log_method(),
                route=self.request_log_route(
                    str(getattr(self, "path", ""))
                ),
                duration_ms=max(
                    0,
                    round((time.perf_counter() - started) * 1000),
                ),
                request_id=getattr(
                    self,
                    "_ytqjk_request_id",
                    "unknown",
                ),
                reason="UNHANDLED_REQUEST_ERROR",
            )
            raise

    def end_headers(self) -> None:
        request_id = getattr(self, "_ytqjk_request_id", secrets.token_hex(8))
        self.send_header("X-Request-ID", request_id)
        super().end_headers()

    def log_error(self, _format: str, *_args: object) -> None:
        return

    def log_message(self, _format: str, *args: object) -> None:
        if len(args) < 2:
            return
        try:
            status = int(args[1])
        except (TypeError, ValueError):
            return
        started = getattr(self, "_ytqjk_request_started", time.perf_counter())
        duration_ms = max(0, round((time.perf_counter() - started) * 1000))
        route = self.request_log_route(str(getattr(self, "path", "")))
        log_event(
            get_logger(self.request_log_component),
            logging.INFO,
            "http_request",
            method=self._request_log_method(),
            route=route,
            status=status,
            duration_ms=duration_ms,
            request_id=getattr(self, "_ytqjk_request_id", "unknown"),
        )

    def request_log_route(self, _path: str) -> str:
        return "/unknown"

    def _request_log_method(self) -> str:
        method = str(getattr(self, "command", "")).upper()
        return method if method in _METHODS else "OTHER"


def configure_logging(
    log_path: Path,
    *,
    component: str,
    level: str | int | None = None,
    max_bytes: int = DEFAULT_MAX_BYTES,
    backup_count: int = DEFAULT_BACKUP_COUNT,
) -> logging.Logger:
    if max_bytes <= 0 or backup_count < 0:
        raise ValueError("invalid log rotation configuration")
    path = Path(log_path).resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    numeric_level = _resolve_level(level)
    base = logging.getLogger(LOGGER_NAME)
    with _CONFIG_LOCK:
        base.disabled = False
        base.setLevel(numeric_level)
        base.propagate = False
        managed = [
            handler for handler in base.handlers
            if getattr(handler, _MANAGED, False)
        ]
        reusable = next((
            handler for handler in managed
            if isinstance(handler, RotatingFileHandler)
            and Path(handler.baseFilename) == path
            and handler.maxBytes == max_bytes
            and handler.backupCount == backup_count
        ), None)
        for handler in managed:
            if handler is reusable:
                handler.setLevel(numeric_level)
                continue
            base.removeHandler(handler)
            handler.close()
        if reusable is None:
            handler = RotatingFileHandler(
                path,
                maxBytes=max_bytes,
                backupCount=backup_count,
                encoding="utf-8",
                delay=True,
            )
            setattr(handler, _MANAGED, True)
            handler.setLevel(numeric_level)
            handler.setFormatter(_EventFormatter())
            base.addHandler(handler)
    return get_logger(component)


def get_logger(component: str) -> logging.Logger:
    safe_component = re.sub(r"[^a-zA-Z0-9_.-]+", "-", component).strip(".")
    return logging.getLogger(f"{LOGGER_NAME}.{safe_component or 'runtime'}")


def log_event(
    logger: logging.Logger,
    level: int,
    event: str,
    **fields: object,
) -> None:
    logger.log(
        level,
        event,
        extra={
            "ytqjk_event": event,
            "ytqjk_fields": {
                key: value for key, value in fields.items()
                if key in _ALLOWED_FIELDS
            },
        },
    )


def log_exception(
    logger: logging.Logger,
    event: str,
    error: BaseException,
    **fields: object,
) -> None:
    values = {**fields, "error_type": type(error).__name__}
    logger.error(
        event,
        extra={
            "ytqjk_event": event,
            "ytqjk_fields": {
                key: value for key, value in values.items()
                if key in _ALLOWED_FIELDS
            },
        },
        exc_info=(type(error), error, error.__traceback__),
    )


def shutdown_logging() -> None:
    base = logging.getLogger(LOGGER_NAME)
    with _CONFIG_LOCK:
        for handler in tuple(base.handlers):
            if not getattr(handler, _MANAGED, False):
                continue
            base.removeHandler(handler)
            handler.close()
        base.setLevel(logging.WARNING)


def _resolve_level(level: str | int | None) -> int:
    if isinstance(level, int) and level >= 0:
        return level
    raw = str(level or os.environ.get(LOG_LEVEL_ENV, "INFO")).upper()
    resolved = logging.getLevelNamesMapping().get(raw)
    return resolved if isinstance(resolved, int) else logging.INFO


def _safe_value(value: object) -> str:
    if isinstance(value, bool):
        return str(value).lower()
    text = str(value)
    text = re.sub(r"\s+", "_", text)
    text = re.sub(r"[^\w./:{}@+-]", "_", text, flags=re.UNICODE)
    return text[:160] or "unknown"


__all__ = [
    "DEFAULT_BACKUP_COUNT",
    "DEFAULT_MAX_BYTES",
    "SafeHttpLogMixin",
    "configure_logging",
    "get_logger",
    "log_event",
    "log_exception",
    "shutdown_logging",
]
