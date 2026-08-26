"""Dashboard and optional LAN peer server lifecycle."""

from __future__ import annotations

import logging
import secrets
import time
from http.server import ThreadingHTTPServer
from pathlib import Path
from threading import Lock, Thread
from typing import Callable

from knowledge_dashboard import KnowledgeHandler
from knowledge_peer_server import start_peer_server
from runtime_logging import get_logger, log_event, log_exception


LOGGER = get_logger("dashboard.runtime")


def serve_dashboard(
    root: Path,
    port: int,
    marker: Path,
    restart_scheduler: Callable[[], None],
) -> None:
    peer_status: dict[str, object] = {"status": "DISABLED"}
    peer_server = None
    try:
        peer_runtime = start_peer_server(root)
        if peer_runtime is not None:
            peer_server, _ = peer_runtime
            address = peer_server.server_address
            peer_status = {
                "status": "LISTENING",
                "bind_host": str(address[0]),
                "port": int(address[1]),
            }
            log_event(
                LOGGER,
                logging.INFO,
                "peer_service_started",
                port=int(address[1]),
            )
        else:
            log_event(LOGGER, logging.INFO, "peer_service_disabled")
    except (OSError, RuntimeError, ValueError) as error:
        peer_status = {
            "status": "FAILED",
            "reason": "PEER_SERVICE_START_FAILED",
        }
        log_exception(
            LOGGER,
            "peer_service_start_failed",
            error,
            reason="PEER_SERVICE_START_FAILED",
        )
    attributes = {
        "knowledge_root": root,
        "plugin_root": Path(__file__).resolve().parent.parents[2],
        "update_lock": Lock(),
        "update_token": secrets.token_urlsafe(32),
        "schedule_restart": staticmethod(restart_scheduler),
        "peer_runtime_status": peer_status,
    }
    handler = type("RootHandler", (KnowledgeHandler,), attributes)
    server = ThreadingHTTPServer(("127.0.0.1", port), handler)
    log_event(
        LOGGER,
        logging.INFO,
        "dashboard_service_started",
        port=port,
    )

    def wait_for_stop() -> None:
        while not marker.exists():
            time.sleep(0.2)
        server.shutdown()

    Thread(target=wait_for_stop, daemon=True).start()
    try:
        server.serve_forever()
    finally:
        server.server_close()
        if peer_server is not None:
            peer_server.shutdown()
            peer_server.server_close()
            log_event(LOGGER, logging.INFO, "peer_service_stopped")
        log_event(
            LOGGER,
            logging.INFO,
            "dashboard_service_stopped",
            port=port,
        )


__all__ = ["serve_dashboard"]
