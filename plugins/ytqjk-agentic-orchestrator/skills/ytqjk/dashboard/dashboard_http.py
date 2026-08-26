from __future__ import annotations

import json
import mimetypes
from http import HTTPStatus
from pathlib import Path
from urllib.parse import unquote, urlparse

from runtime_logging import SafeHttpLogMixin


CONTENT_SECURITY_POLICY = (
    "default-src 'self'; base-uri 'none'; form-action 'none'; "
    "frame-ancestors 'none'"
)
_DASHBOARD_ROUTES = frozenset({
    "/api/candidate",
    "/api/candidate/approve",
    "/api/document",
    "/api/global-library",
    "/api/intake",
    "/api/project-library",
    "/api/snapshot",
    "/api/update",
})


class DashboardHttpMixin(SafeHttpLogMixin):
    dashboard_dir: Path
    max_intake_bytes: int
    request_log_component = "dashboard.http"

    def request_log_route(self, request_path: str) -> str:
        path = urlparse(request_path).path
        if path.startswith("/api/intake/jobs/"):
            return "/api/intake/jobs/{job_id}"
        if path.startswith("/api/tree/"):
            return "/api/tree/{operation}"
        if path in _DASHBOARD_ROUTES:
            return path
        return "/api/{unknown}" if path.startswith("/api/") else "/assets"

    def api_host_allowed(self) -> bool:
        host = self.headers.get("Host", "")
        try:
            parsed = urlparse(f"//{host}")
            expected_port = int(self.server.server_address[1])
            port = parsed.port if parsed.port is not None else 80
        except (AttributeError, TypeError, ValueError):
            return False
        return (
            parsed.hostname in {"127.0.0.1", "localhost"}
            and parsed.username is None
            and parsed.password is None
            and port == expected_port
        )

    def api_write_allowed(self) -> bool:
        if not self.api_host_allowed():
            return False
        host = self.headers.get("Host", "")
        origin = self.headers.get("Origin", "")
        content_type = self.headers.get("Content-Type", "")
        try:
            parsed = urlparse(origin)
            host_name = urlparse(f"//{host}").hostname
            expected_port = int(self.server.server_address[1])
            port = parsed.port if parsed.port is not None else 80
        except (AttributeError, TypeError, ValueError):
            return False
        return (
            parsed.scheme == "http"
            and parsed.hostname == host_name
            and port == expected_port
            and not parsed.query
            and not parsed.fragment
            and parsed.path in {"", "/"}
            and not parsed.params
            and parsed.username is None
            and parsed.password is None
            and content_type.split(";", 1)[0].strip()
            == "application/json"
        )

    def read_payload(self, length: int | None = None) -> dict[str, object]:
        content_length = length
        if content_length is None:
            content_length = int(self.headers.get("Content-Length", "0"))
        limit = (self.max_intake_bytes * 4 // 3) + 8192
        if not 0 < content_length <= limit:
            raise ValueError("请求长度无效。")
        payload = json.loads(self.rfile.read(content_length).decode("utf-8"))
        if not isinstance(payload, dict):
            raise ValueError("请求格式错误。")
        return payload

    def serve_asset(self, request_path: str) -> None:
        requested = (
            "index.html"
            if request_path in {"", "/"}
            else unquote(request_path).lstrip("/")
        )
        path = (self.dashboard_dir / requested).resolve()
        inside = self.dashboard_dir in path.parents
        if not path.is_file() or not inside:
            self.send_error(HTTPStatus.NOT_FOUND, "Asset not found")
            return
        content = path.read_bytes()
        self.send_response(HTTPStatus.OK)
        content_type = mimetypes.guess_type(str(path))[0]
        self.send_header(
            "Content-Type",
            content_type or "application/octet-stream",
        )
        self.send_header("Content-Security-Policy", CONTENT_SECURITY_POLICY)
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(content)))
        self.end_headers()
        self.wfile.write(content)

    def send_json(
        self,
        value: dict[str, object],
        status: int | HTTPStatus = HTTPStatus.OK,
    ) -> None:
        body = json.dumps(value, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Security-Policy", CONTENT_SECURITY_POLICY)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
        self.wfile.flush()
