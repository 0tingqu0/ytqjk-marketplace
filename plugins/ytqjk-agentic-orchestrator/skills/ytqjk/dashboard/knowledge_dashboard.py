from __future__ import annotations

import argparse
import json
import mimetypes
import sys
from http import HTTPStatus
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlparse

DASHBOARD_DIR = Path(__file__).resolve().parent
SCRIPTS_DIR = DASHBOARD_DIR.parent / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

from platform_paths import default_knowledge_root  # noqa: E402


MAX_PREVIEW_CHARS = 24_000
SECTIONS = (
    ("verified", "已验证", "verified"),
    ("personal-experience/approved", "个人经验", "approved"),
    ("error-experience/approved", "错误经验", "approved"),
    ("personal-experience/candidates", "个人候选", "candidate"),
    ("error-experience/candidates", "错误候选", "candidate"),
)


def read_json(path: Path) -> dict[str, object]:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def relative_files(root: Path, relative: str, label: str, state: str) -> list[dict[str, object]]:
    directory = root / relative
    if not directory.is_dir():
        return []
    rows = []
    for path in sorted(directory.rglob("*.md")):
        display_path = path.relative_to(root).as_posix()
        if safe_document(root, display_path) is None:
            continue
        rows.append(
            {
                "path": display_path,
                "label": label,
                "state": state,
                "bytes": path.stat().st_size,
                "modified": path.stat().st_mtime,
            }
        )
    return rows


def project_rows(root: Path) -> list[dict[str, object]]:
    rows = []
    for manifest_path in sorted((root / "projects").glob("*/manifest.json")):
        manifest = read_json(manifest_path)
        identity = manifest.get("identity", {})
        stats = manifest.get("stats", {})
        vector = manifest.get("vector", {})
        if not isinstance(identity, dict) or not isinstance(stats, dict):
            continue
        rows.append(
            {
                "id": identity.get("id", manifest_path.parent.name),
                "name": identity.get("name", manifest_path.parent.name),
                "head": identity.get("head", "UNKNOWN"),
                "dirty": identity.get("dirty", "unknown"),
                "indexed_at": manifest.get("indexed_at"),
                "files": stats.get("files", 0),
                "chunks": stats.get("chunks", 0),
                "text_bytes": stats.get("text_bytes", 0),
                "vector": vector.get("status", "NOT_BUILT") if isinstance(vector, dict) else "UNKNOWN",
            }
        )
    return rows


def snapshot(root: Path) -> dict[str, object]:
    documents = [
        row for relative, label, state in SECTIONS for row in relative_files(root, relative, label, state)
    ]
    global_manifest = read_json(root / "global-cache" / "manifest.json")
    return {
        "root": str(root),
        "config": read_json(root / "config.json"),
        "global": global_manifest,
        "projects": project_rows(root),
        "documents": documents,
        "counts": {
            "verified": sum(item["state"] == "verified" for item in documents),
            "approved": sum(item["state"] == "approved" for item in documents),
            "candidate": sum(item["state"] == "candidate" for item in documents),
        },
    }


def safe_document(root: Path, raw_path: str) -> Path | None:
    candidate = (root / raw_path).resolve()
    try:
        candidate.relative_to(root.resolve())
    except ValueError:
        return None
    return candidate if candidate.suffix == ".md" and candidate.is_file() else None


class KnowledgeHandler(SimpleHTTPRequestHandler):
    knowledge_root: Path

    def do_GET(self) -> None:  # noqa: N802 - inherited API name
        url = urlparse(self.path)
        if url.path == "/api/snapshot":
            self.send_json(snapshot(self.knowledge_root))
            return
        if url.path == "/api/document":
            path = safe_document(self.knowledge_root, parse_qs(url.query).get("path", [""])[0])
            if path is None:
                self.send_error(HTTPStatus.NOT_FOUND, "Document not found")
                return
            self.send_json({"path": path.relative_to(self.knowledge_root).as_posix(), "content": path.read_text(encoding="utf-8")[:MAX_PREVIEW_CHARS]})
            return
        self.serve_asset(url.path)

    def serve_asset(self, request_path: str) -> None:
        requested = "index.html" if request_path in {"", "/"} else unquote(request_path).lstrip("/")
        path = (DASHBOARD_DIR / requested).resolve()
        if not path.is_file() or DASHBOARD_DIR not in path.parents and path != DASHBOARD_DIR:
            self.send_error(HTTPStatus.NOT_FOUND, "Asset not found")
            return
        content = path.read_bytes()
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", mimetypes.guess_type(str(path))[0] or "application/octet-stream")
        self.send_header("Content-Length", str(len(content)))
        self.end_headers()
        self.wfile.write(content)

    def send_json(self, value: dict[str, object]) -> None:
        body = json.dumps(value, ensure_ascii=False).encode("utf-8")
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Security-Policy", "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, _format: str, *_args: object) -> None:
        return


def main() -> None:
    parser = argparse.ArgumentParser(description="Read-only YTQJK knowledge dashboard.")
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    parser.add_argument("--port", type=int, default=8765)
    args = parser.parse_args()
    root = args.knowledge_root.resolve()
    handler = type("RootHandler", (KnowledgeHandler,), {"knowledge_root": root})
    server = ThreadingHTTPServer(("127.0.0.1", args.port), handler)
    print(f"Knowledge dashboard: http://127.0.0.1:{args.port}")
    server.serve_forever()


if __name__ == "__main__":
    main()
