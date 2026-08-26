from __future__ import annotations

import json
import socket
import subprocess
import sys
import tempfile
import urllib.request
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
SERVICE = (
    ROOT / "plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard"
    / "dashboard_service.py"
)


def free_port() -> int:
    with socket.socket() as current:
        current.bind(("127.0.0.1", 0))
        return int(current.getsockname()[1])


def test_dashboard_logs_safe_request_summary_and_keeps_json_stdout() -> None:
    port = free_port()
    marker = "PRIVATE_QUERY_MARKER"
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary) / "knowledge"
        common = [
            "--knowledge-root", str(root),
            "--port", str(port),
            "--document-runtime", "off",
        ]
        started = subprocess.run(
            [sys.executable, str(SERVICE), "start", *common],
            capture_output=True,
            text=True,
            encoding="utf-8",
            check=False,
            timeout=20,
        )
        assert started.returncode == 0, started.stderr or started.stdout
        assert json.loads(started.stdout)["status"] == "RUNNING"

        try:
            url = (
                f"http://127.0.0.1:{port}/api/snapshot?secret={marker}"
            )
            with urllib.request.urlopen(url, timeout=3) as response:
                json.load(response)
                request_id = response.headers["X-Request-ID"]
            assert len(request_id) == 16
        finally:
            stopped = subprocess.run(
                [sys.executable, str(SERVICE), "stop", *common],
                capture_output=True,
                text=True,
                encoding="utf-8",
                check=False,
                timeout=20,
            )
            assert stopped.returncode == 0, stopped.stderr or stopped.stdout

        content = (
            root / "service" / "dashboard-service.log"
        ).read_text(encoding="utf-8")
        assert "event=dashboard_service_started" in content
        assert "event=http_request" in content
        assert "route=/api/snapshot" in content
        assert f"request_id={request_id}" in content
        assert marker not in content
