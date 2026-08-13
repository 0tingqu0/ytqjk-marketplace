"""Loopback-only HTTP workbench using KnowledgeService public methods."""

from __future__ import annotations

import argparse
import json
import secrets
from dataclasses import dataclass
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Callable
from urllib.parse import urlparse

from scripts.service import KnowledgeService
@dataclass(frozen=True)
class WorkbenchContext:
    """Trusted local service and project scope."""

    service: KnowledgeService
    project_id: str
    csrf_token: str


class WorkbenchServer(ThreadingHTTPServer):
    """Loopback server carrying immutable adapter context."""

    def __init__(
        self, address: tuple[str, int], context: WorkbenchContext
    ) -> None:
        if address[0] != "127.0.0.1":
            raise ValueError("workbench only permits 127.0.0.1")
        super().__init__(address, WorkbenchHandler)
        self.context = context
        self.created_documents: set[str] = set()

class WorkbenchHandler(BaseHTTPRequestHandler):
    """Serve static UI and stable local JSON contract."""

    server: WorkbenchServer
    assets = Path(__file__).with_name("static")

    def do_GET(self) -> None:  # noqa: N802
        if not self._host_allowed():
            return
        parsed = urlparse(self.path)
        if parsed.path == "/api/state":
            self._json(HTTPStatus.OK, self._state())
            return
        if parsed.path == "/":
            self._asset("index.html", "text/html; charset=utf-8")
            return
        if parsed.path in {"/app.css", "/app.js"}:
            mime = (
                "text/css; charset=utf-8"
                if parsed.path.endswith("css")
                else "text/javascript; charset=utf-8"
            )
            self._asset(parsed.path[1:], mime)
            return
        self._error(HTTPStatus.NOT_FOUND, "NOT_FOUND")

    def do_POST(self) -> None:  # noqa: N802
        if not self._host_allowed() or not self._write_allowed():
            return
        body = self._body()
        if body is None:
            return
        routes: dict[str, Callable[[dict[str, Any]], dict[str, Any]]] = {
            "/api/candidates": self._create_candidate,
            "/api/candidates/edit": self._edit_candidate,
            "/api/candidates/delete": self._delete_candidate,
            "/api/candidates/approve": self._approve_candidate,
        }
        action = routes.get(urlparse(self.path).path)
        if action is None:
            self._error(HTTPStatus.NOT_FOUND, "NOT_FOUND")
            return
        try:
            response = action(body)
        except (RuntimeError, ValueError, KeyError):
            self._error(HTTPStatus.BAD_REQUEST, "INVALID_REQUEST")
            return
        if response["status"] == "NOT_CONFIGURED":
            self._json(HTTPStatus.CONFLICT, response)
            return
        self._json(HTTPStatus.OK, response)

    def _state(self) -> dict[str, Any]:
        context = self.server.context
        project = context.service.project(context.project_id)
        active = context.service.read_active_snapshot(context.project_id)
        snapshot = active["snapshot"] if active else None
        snapshot_versions = active["versions"] if active else []
        snapshot_documents = self._documents(snapshot_versions)
        pending = self._pending_documents()
        return {
            "project": {key: project[key] for key in ("id", "scope", "alias")},
            "snapshot": self._snapshot(snapshot),
            "snapshot_documents": snapshot_documents,
            "versions": sum(
                (item["versions"] for item in snapshot_documents), []
            ),
            "project_pending_candidates": {
                "state": "PROCESS_LOCAL",
                "restart_recovery": "NOT_CONFIGURED",
                "items": pending,
            },
            "writer_jobs": {"state": "NOT_CONFIGURED", "items": []},
            "intake_jobs": {"state": "NOT_CONFIGURED", "items": []},
            "retrieval": {
                "state": "NOT_CONFIGURED", "results": [], "citations": []
            },
            "governance": {
                "state": "NOT_CONFIGURED", "promotion": "FAIL_CLOSED"
            },
            "csrf_token": context.csrf_token,
        }

    def _documents(self, members: list[dict[str, Any]]) -> list[dict[str, Any]]:
        service = self.server.context.service
        items: list[dict[str, Any]] = []
        for member in members:
            versions = service.document_versions(str(member["document_id"]))
            fixed = [
                self._version(row)
                for row in versions
                if row["id"] == member["version_id"]
            ]
            items.append({"id": member["document_id"], "versions": fixed})
        return items

    def _pending_documents(self) -> list[dict[str, Any]]:
        service = self.server.context.service
        items: list[dict[str, Any]] = []
        for document_id in sorted(self.server.created_documents):
            versions = service.document_versions(document_id)
            if versions and versions[-1]["state"] == "candidate":
                items.append({"id": document_id, "versions": [
                    self._version(versions[-1])
                ]})
        return items

    @staticmethod
    def _version(row: dict[str, Any]) -> dict[str, Any]:
        keys = ("id", "document_id", "ordinal", "state", "created_at")
        return {key: row[key] for key in keys}

    @staticmethod
    def _snapshot(snapshot: dict[str, Any] | None) -> dict[str, Any]:
        if snapshot is None:
            return {"state": "NOT_CONFIGURED", "generation": None}
        keys = ("id", "project_id", "generation", "state", "created_at")
        return {key: snapshot[key] for key in keys}

    def _create_candidate(self, body: dict[str, Any]) -> dict[str, Any]:
        service = self.server.context.service
        document_id = service.create_candidate(
            self.server.context.project_id,
            self._text(body, "title"),
            self._text(body, "content"),
            self._text(body, "source"),
        )
        self.server.created_documents.add(document_id)
        return {"status": "CANDIDATE_CREATED", "document_id": document_id}

    def _edit_candidate(self, body: dict[str, Any]) -> dict[str, Any]:
        document_id = self._owned_candidate(body)
        self.server.context.service.edit_candidate(
            document_id,
            self._text(body, "content"),
            self._text(body, "source"),
        )
        return {"status": "CANDIDATE_UPDATED"}

    def _delete_candidate(self, body: dict[str, Any]) -> dict[str, Any]:
        document_id = self._owned_candidate(body)
        self.server.context.service.soft_delete_candidate(document_id)
        self.server.created_documents.discard(document_id)
        return {"status": "CANDIDATE_DELETED"}

    def _approve_candidate(self, body: dict[str, Any]) -> dict[str, Any]:
        self._owned_candidate(body)
        return {"status": "NOT_CONFIGURED", "promotion": "FAIL_CLOSED"}

    def _owned_candidate(self, body: dict[str, Any]) -> str:
        """Prove document belongs to trusted project before mutation."""
        document_id = self._text(body, "document_id")
        versions = self.server.context.service.document_versions(document_id)
        active = self.server.context.service.read_active_snapshot(
            self.server.context.project_id
        )
        members = active["versions"] if active else []
        in_snapshot = any(
            member["document_id"] == document_id for member in members
        )
        if not versions or versions[-1]["state"] != "candidate":
            raise ValueError("candidate is unavailable")
        if document_id not in self.server.created_documents and not in_snapshot:
            raise ValueError("candidate project does not match context")
        return document_id

    def _host_allowed(self) -> bool:
        host = self.headers.get("Host", "")
        expected = f"127.0.0.1:{self.server.server_port}"
        if host != expected:
            self._error(HTTPStatus.FORBIDDEN, "LOCAL_HOST_REQUIRED")
            return False
        return True

    def _write_allowed(self) -> bool:
        expected = f"127.0.0.1:{self.server.server_port}"
        origin = self.headers.get("Origin")
        if origin != f"http://{expected}":
            self._error(HTTPStatus.FORBIDDEN, "LOCAL_ORIGIN_REQUIRED")
            return False
        token = self.headers.get("X-CSRF-Token", "")
        if not secrets.compare_digest(token, self.server.context.csrf_token):
            self._error(HTTPStatus.FORBIDDEN, "CSRF_REQUIRED")
            return False
        return True

    def _body(self) -> dict[str, Any] | None:
        try:
            length = int(self.headers.get("Content-Length", "0"))
            value = json.loads(self.rfile.read(min(length, 65536)))
        except (TypeError, ValueError, json.JSONDecodeError):
            self._error(HTTPStatus.BAD_REQUEST, "INVALID_JSON")
            return None
        if not isinstance(value, dict):
            self._error(HTTPStatus.BAD_REQUEST, "INVALID_JSON")
            return None
        return value

    @staticmethod
    def _text(body: dict[str, Any], key: str) -> str:
        value = body.get(key)
        if (
            not isinstance(value, str)
            or not value.strip()
            or len(value) > 16384
        ):
            raise ValueError(key)
        return value.strip()

    def _asset(self, name: str, mime: str) -> None:
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", mime)
        self.send_header(
            "Content-Security-Policy",
            "default-src 'self'; connect-src 'self'; style-src 'self'; "
            "script-src 'self'",
        )
        self.end_headers()
        self.wfile.write((self.assets / name).read_bytes())

    def _json(self, status: HTTPStatus, payload: dict[str, Any]) -> None:
        data = json.dumps(
            payload, ensure_ascii=True, separators=(",", ":")
        ).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(data)

    def _error(self, status: HTTPStatus, code: str) -> None:
        self._json(status, {"error": {"code": code}})

    def log_message(self, _: str, *args: Any) -> None:
        return


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Local YTQJK knowledge workbench"
    )
    parser.add_argument("database", type=Path)
    parser.add_argument("project_id")
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=0)
    args = parser.parse_args()
    if args.host != "127.0.0.1" or not 0 <= args.port <= 65535:
        parser.error("only 127.0.0.1 and ports 0..65535 are permitted")
    context = WorkbenchContext(
        KnowledgeService(args.database),
        args.project_id,
        secrets.token_urlsafe(32),
    )
    with WorkbenchServer((args.host, args.port), context) as server:
        address = f"http://127.0.0.1:{server.server_port}"
        print(json.dumps({"address": address, "bind": "127.0.0.1"}), flush=True)
        server.serve_forever()


if __name__ == "__main__":
    main()
