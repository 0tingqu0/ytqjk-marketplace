from __future__ import annotations

import http.client
import logging
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from runtime_logging import (  # noqa: E402
    SafeHttpLogMixin,
    configure_logging,
    log_event,
    log_exception,
    shutdown_logging,
)


@pytest.fixture(autouse=True)
def close_logging() -> None:
    yield
    shutdown_logging()


def test_logging_is_utf8_and_drops_unapproved_fields(
    tmp_path: Path,
) -> None:
    path = tmp_path / "runtime.log"
    logger = configure_logging(path, component="dashboard")

    log_event(
        logger,
        logging.INFO,
        "dashboard_service_started",
        port=8765,
        reason="READY",
        content="PRIVATE_CONTENT_MARKER",
    )
    shutdown_logging()

    content = path.read_text(encoding="utf-8")
    assert "event=dashboard_service_started" in content
    assert "port=8765" in content
    assert "reason=READY" in content
    assert "PRIVATE_CONTENT_MARKER" not in content


def test_environment_level_and_repeated_configuration_are_safe(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    path = tmp_path / "runtime.log"
    monkeypatch.setenv("YTQJK_LOG_LEVEL", "WARNING")
    first = configure_logging(path, component="dashboard")
    second = configure_logging(path, component="dashboard")

    assert first is second
    log_event(first, logging.INFO, "ignored_info")
    log_event(first, logging.WARNING, "kept_warning")
    shutdown_logging()

    content = path.read_text(encoding="utf-8")
    assert "ignored_info" not in content
    assert content.count("event=kept_warning") == 1


def test_log_rotation_keeps_configured_number_of_backups(
    tmp_path: Path,
) -> None:
    path = tmp_path / "runtime.log"
    logger = configure_logging(
        path,
        component="dashboard",
        max_bytes=180,
        backup_count=2,
    )

    for index in range(30):
        log_event(
            logger,
            logging.INFO,
            "http_request",
            request_id=f"{index:016x}",
            route="/api/snapshot",
            status=200,
        )
    shutdown_logging()

    files = sorted(tmp_path.glob("runtime.log*"))
    assert path.with_name("runtime.log.1") in files
    assert len(files) <= 3


def test_exception_trace_omits_message_and_absolute_path(
    tmp_path: Path,
) -> None:
    path = tmp_path / "runtime.log"
    logger = configure_logging(path, component="dashboard")

    try:
        raise RuntimeError("PRIVATE_EXCEPTION_MARKER C:\\private\\file")
    except RuntimeError as error:
        log_exception(logger, "dashboard_service_failed", error)
    shutdown_logging()

    content = path.read_text(encoding="utf-8")
    assert "RuntimeError" in content
    assert "PRIVATE_EXCEPTION_MARKER" not in content
    assert "C:\\private" not in content


def test_unhandled_http_error_is_logged_without_sensitive_details(
    tmp_path: Path,
) -> None:
    class FailingHandler(SafeHttpLogMixin, BaseHTTPRequestHandler):
        request_log_component = "test-http"

        def do_GET(self) -> None:
            raise RuntimeError(
                "PRIVATE_REQUEST_EXCEPTION C:\\private\\request"
            )

        def request_log_route(self, _path: str) -> str:
            return "/failure"

    class QuietServer(ThreadingHTTPServer):
        def handle_error(
            self,
            _request: object,
            _client_address: object,
        ) -> None:
            return

    path = tmp_path / "runtime.log"
    configure_logging(path, component="test-http")
    server = QuietServer(("127.0.0.1", 0), FailingHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    connection = http.client.HTTPConnection(
        *server.server_address,
        timeout=2,
    )

    try:
        connection.request(
            "GET",
            "/failure?token=PRIVATE_QUERY_MARKER",
        )
        with pytest.raises(
            (http.client.RemoteDisconnected, ConnectionResetError)
        ):
            connection.getresponse()
    finally:
        connection.close()
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
        shutdown_logging()

    content = path.read_text(encoding="utf-8")
    assert "event=http_request_failed" in content
    assert "method=GET" in content
    assert "route=/failure" in content
    assert "reason=UNHANDLED_REQUEST_ERROR" in content
    assert "request_id=" in content
    assert "exception=RuntimeError" in content
    assert "PRIVATE_REQUEST_EXCEPTION" not in content
    assert "PRIVATE_QUERY_MARKER" not in content
    assert "C:\\private\\request" not in content
