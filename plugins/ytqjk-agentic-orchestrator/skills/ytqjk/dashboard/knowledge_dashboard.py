from __future__ import annotations

import argparse
import base64
import binascii
import json
import secrets
import sys
from http import HTTPStatus
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from threading import Lock
from urllib.parse import parse_qs, urlparse

DASHBOARD_DIR = Path(__file__).resolve().parent
SCRIPTS_DIR = DASHBOARD_DIR.parent / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))
sys.path.insert(0, str(DASHBOARD_DIR))

from approval_promotion import promote  # noqa: E402
from candidate_actions import (  # noqa: E402
    candidate_document,
    delete_candidate,
    update_candidate,
)
from dashboard_candidate_http import (  # noqa: E402
    document_payload,
    update_payload,
)
from dashboard_api_routes import (  # noqa: E402
    handle_dashboard_get, handle_dashboard_post,
)
from dashboard_documents import build_snapshot, global_index_library  # noqa: E402
from dashboard_documents import project_library, safe_document, snapshot  # noqa: E402
from dashboard_intake_api import (  # noqa: E402
    DashboardIntakeApi, IntakeApiError,
    intake_document,
    intake_upload,
)
from dashboard_http import DashboardHttpMixin  # noqa: E402
from dashboard_update_http import (  # noqa: E402
    handle_update_request,
    send_update_status,
)
from platform_paths import default_knowledge_root  # noqa: E402

MAX_PREVIEW_CHARS = 24_000
MAX_INTAKE_BYTES = 10 * 1024 * 1024


class KnowledgeHandler(DashboardHttpMixin, SimpleHTTPRequestHandler):
    dashboard_dir = DASHBOARD_DIR
    max_intake_bytes = MAX_INTAKE_BYTES
    knowledge_root: Path
    plugin_root: Path
    update_lock: object
    update_token: str
    schedule_restart: object
    intake_api: object | None = None
    tree_api: object | None = None
    restart_after_response = False

    def finish(self) -> None:
        super().finish()
        if self.restart_after_response:
            self.restart_after_response = False
            self.schedule_restart()

    def do_GET(self) -> None:  # noqa: N802 - inherited API name
        url = urlparse(self.path)
        if url.path.startswith("/api/") and not self.api_host_allowed():
            self.send_json(
                {"ok": False, "error": "Forbidden host"},
                HTTPStatus.FORBIDDEN,
            )
            return
        if handle_dashboard_get(self, url.path, url.query):
            return
        if url.path.startswith("/api/intake/jobs/"):
            try:
                self.send_json(self.intake().get(url.path))
            except IntakeApiError as exc:
                self.send_json(
                    {"ok": False, "error": str(exc)}, exc.status
                )
            return
        if url.path == "/api/snapshot":
            self.send_json(snapshot(self.knowledge_root))
            return
        if url.path == "/api/update":
            send_update_status(self)
            return
        if url.path == "/api/global-library":
            self.send_json(global_index_library(self.knowledge_root))
            return
        if url.path == "/api/project-library":
            project_id = parse_qs(url.query).get("id", [""])[0]
            project = project_library(self.knowledge_root, project_id)
            if project is None:
                self.send_error(
                    HTTPStatus.NOT_FOUND,
                    "Project library not found",
                )
                return
            self.send_json(project)
            return
        if url.path == "/api/document":
            document = document_payload(
                self.knowledge_root,
                parse_qs(url.query).get("path", [""])[0],
                MAX_PREVIEW_CHARS,
            )
            if document is None:
                self.send_error(HTTPStatus.NOT_FOUND, "Document not found")
                return
            self.send_json(document)
            return
        self.serve_asset(url.path)

    def do_PUT(self) -> None:  # noqa: N802 - inherited API name
        if not self.api_write_allowed():
            self.send_json(
                {"ok": False, "error": "Forbidden request"},
                HTTPStatus.FORBIDDEN,
            )
            return
        if urlparse(self.path).path != "/api/candidate":
            self.send_error(HTTPStatus.NOT_FOUND, "API not found")
            return
        try:
            payload = self.read_payload()
            self.send_json(update_payload(self.knowledge_root, payload))
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            status = (
                HTTPStatus.CONFLICT
                if str(exc) == "CANDIDATE_VERSION_CONFLICT"
                else HTTPStatus.BAD_REQUEST
            )
            self.send_json({"ok": False, "error": str(exc)}, status)

    def do_DELETE(self) -> None:  # noqa: N802 - inherited API name
        if not self.api_write_allowed():
            self.send_json(
                {"ok": False, "error": "Forbidden request"},
                HTTPStatus.FORBIDDEN,
            )
            return
        if urlparse(self.path).path != "/api/candidate":
            self.send_error(HTTPStatus.NOT_FOUND, "API not found")
            return
        try:
            payload = self.read_payload()
            path = payload.get("path")
            if not isinstance(path, str):
                raise ValueError("候选资料路径无效。")
            delete_candidate(self.knowledge_root, path)
            self.send_json({"ok": True})
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            self.send_json(
                {"ok": False, "error": str(exc)},
                HTTPStatus.BAD_REQUEST,
            )

    def do_POST(self) -> None:  # noqa: N802 - inherited API name
        request_path = urlparse(self.path).path
        if not self.api_write_allowed():
            self.send_json(
                {"ok": False, "error": "Forbidden request"},
                HTTPStatus.FORBIDDEN,
            )
            return
        if handle_dashboard_post(self, request_path):
            return
        if request_path.startswith("/api/intake/jobs/"):
            try:
                status, result = self.intake().action(request_path)
                self.send_json(result, status)
            except IntakeApiError as exc:
                self.send_json(
                    {"ok": False, "error": str(exc)}, exc.status
                )
            return
        if urlparse(self.path).path == "/api/update":
            handle_update_request(self)
            return
        if request_path == "/api/candidate/approve":
            try:
                payload = self.read_payload()
                raw_path = payload.get("path")
                if not isinstance(raw_path, str):
                    raise ValueError("候选资料路径无效。")
                candidate = candidate_document(self.knowledge_root, raw_path)
                approved = candidate is not None and promote(
                    self.knowledge_root,
                    candidate,
                    require_ready=False,
                )
                if not approved:
                    raise ValueError("候选资料无效或包含敏感内容，不能批准。")
                approved_path = raw_path.replace(
                    "/candidates/",
                    "/approved/",
                )
                self.send_json(
                    {
                        "ok": True,
                        "path": approved_path,
                        "state": "approved",
                    }
                )
            except (
                UnicodeDecodeError,
                json.JSONDecodeError,
                ValueError,
            ) as exc:
                self.send_json(
                    {"ok": False, "error": str(exc)},
                    HTTPStatus.BAD_REQUEST,
                )
            return
        if request_path != "/api/intake":
            self.send_error(HTTPStatus.NOT_FOUND, "API not found")
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_json(
                {"ok": False, "error": "请求长度无效。"},
                HTTPStatus.BAD_REQUEST,
            )
            return
        if not 0 < length <= (MAX_INTAKE_BYTES * 4 // 3) + 8192:
            self.send_error(
                HTTPStatus.REQUEST_ENTITY_TOO_LARGE,
                "Payload too large",
            )
            return
        try:
            payload = self.read_payload(length)
            if DashboardIntakeApi.structured_name(payload):
                self.send_json(
                    self.intake().submit(payload), HTTPStatus.ACCEPTED
                )
                return
            name = payload.get("name")
            content = payload.get("content")
            purpose = payload.get("purpose", "")
            relative_path = payload.get("relativePath", "")
            values = (name, content, purpose, relative_path)
            if not all(isinstance(value, str) for value in values):
                raise ValueError("资料名称或内容无效。")
            if payload.get("encoding") == "base64":
                source = base64.b64decode(content, validate=True)
                result = intake_upload(
                    self.knowledge_root,
                    name,
                    source,
                    purpose,
                    relative_path,
                )
            else:
                result = intake_document(
                    self.knowledge_root,
                    name,
                    content,
                    purpose,
                )
            self.send_json({"ok": True, **result}, HTTPStatus.CREATED)
        except IntakeApiError as exc:
            self.send_json({"ok": False, "error": str(exc)}, exc.status)
        except (binascii.Error, UnicodeDecodeError,
                json.JSONDecodeError, ValueError) as exc:
            self.send_json(
                {"ok": False, "error": str(exc)}, HTTPStatus.BAD_REQUEST
            )

    def intake(self) -> DashboardIntakeApi:
        current = type(self).intake_api
        if current is None:
            with self.update_lock:
                current = type(self).intake_api
                if current is None:
                    current = DashboardIntakeApi(
                        self.knowledge_root, self.plugin_root,
                        max_bytes=MAX_INTAKE_BYTES,
                    )
                    type(self).intake_api = current
        return current


def main() -> None:
    parser = argparse.ArgumentParser(description="YTQJK knowledge dashboard.")
    parser.add_argument(
        "--knowledge-root",
        type=Path,
        default=default_knowledge_root(),
    )
    parser.add_argument("--port", type=int, default=8765)
    args = parser.parse_args()
    root = args.knowledge_root.resolve()
    handler = type("RootHandler", (KnowledgeHandler,), {
        "knowledge_root": root,
        "plugin_root": DASHBOARD_DIR.parents[2],
        "update_lock": Lock(),
        "update_token": secrets.token_urlsafe(32),
        "schedule_restart": staticmethod(lambda: None),
        "intake_api": None,
        "tree_api": None,
    })
    server = ThreadingHTTPServer(("127.0.0.1", args.port), handler)
    print(f"Knowledge dashboard: http://127.0.0.1:{args.port}")
    server.serve_forever()


if __name__ == "__main__":
    main()
