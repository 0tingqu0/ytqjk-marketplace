from __future__ import annotations

import argparse
import json
import mimetypes
import re
import sys
import uuid
from datetime import datetime, timezone
from http import HTTPStatus
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, unquote, urlparse

DASHBOARD_DIR = Path(__file__).resolve().parent
SCRIPTS_DIR = DASHBOARD_DIR.parent / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))

from platform_paths import default_knowledge_root  # noqa: E402
from rag_security import contains_high_confidence_secret, is_sensitive_path  # noqa: E402


MAX_PREVIEW_CHARS = 24_000
MAX_INTAKE_BYTES = 1024 * 1024
INTAKE_DIR = "personal-experience/candidates/imports"
TEXT_EXTENSIONS = {".csv", ".json", ".log", ".md", ".rst", ".txt", ".yaml", ".yml"}
SAFE_FILE_NAME = re.compile(r"[\w .()（）-]+\.[A-Za-z0-9]+", re.UNICODE)
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


def analyze_intake(name: str, content: str) -> dict[str, object]:
    lines = [line.strip() for line in content.splitlines() if line.strip()]
    headings = [line.lstrip("#").strip() for line in lines if line.startswith("#")]
    summary = next((line for line in lines if not line.startswith("---")), "")
    return {
        "format": Path(name).suffix.lower().lstrip("."),
        "bytes": len(content.encode("utf-8")),
        "lines": len(content.splitlines()),
        "title": headings[0] if headings else Path(name).stem,
        "summary": summary[:240],
    }


def intake_document(root: Path, name: str, content: str) -> dict[str, str]:
    source_name = Path(name).name.strip()
    encoded = content.encode("utf-8")
    if (
        not source_name
        or source_name != name
        or SAFE_FILE_NAME.fullmatch(source_name) is None
        or Path(source_name).suffix.lower() not in TEXT_EXTENSIONS
    ):
        raise ValueError("仅支持 .md、.txt、.json、.yaml、.yml、.csv、.log、.rst 文本资料。")
    if is_sensitive_path(source_name) or contains_high_confidence_secret(content):
        raise ValueError("资料可能包含凭据或敏感文件名，未保存。")
    if not content.strip() or "\x00" in content:
        raise ValueError("资料必须是非空 UTF-8 文本。")
    if len(encoded) > MAX_INTAKE_BYTES:
        raise ValueError("单份资料不能超过 1 MiB。")
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    target = root / INTAKE_DIR / f"{timestamp}-{uuid.uuid4().hex[:8]}-{Path(source_name).stem}.md"
    target.parent.mkdir(parents=True, exist_ok=True)
    analysis = analyze_intake(source_name, content)
    metadata = (
        "---\nstatus: CANDIDATE\nsource: dashboard-intake\n"
        f"original_name: {source_name}\nreceived_at: {datetime.now(timezone.utc).isoformat()}\n---\n\n"
    )
    report = (
        f"# 投递候选：{analysis['title']}\n\n## 入库分析\n\n"
        f"- 格式：`{analysis['format']}`\n- 大小：{analysis['bytes']} bytes\n"
        f"- 行数：{analysis['lines']}\n- 摘要：{analysis['summary']}\n\n"
        "此资料尚未验证或批准，仅作为候选知识供后续审阅。\n\n## 原始资料\n\n"
    )
    target.write_text(metadata + report + content, encoding="utf-8")
    return {"path": target.relative_to(root).as_posix(), "state": "candidate"}


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

    def do_POST(self) -> None:  # noqa: N802 - inherited API name
        if urlparse(self.path).path != "/api/intake":
            self.send_error(HTTPStatus.NOT_FOUND, "API not found")
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_json({"ok": False, "error": "请求长度无效。"}, HTTPStatus.BAD_REQUEST)
            return
        if not 0 < length <= MAX_INTAKE_BYTES + 4096:
            self.send_error(HTTPStatus.REQUEST_ENTITY_TOO_LARGE, "Payload too large")
            return
        try:
            payload = json.loads(self.rfile.read(length).decode("utf-8"))
            if not isinstance(payload, dict):
                raise ValueError("请求格式错误。")
            name, content = payload.get("name"), payload.get("content")
            if not isinstance(name, str) or not isinstance(content, str):
                raise ValueError("资料名称或内容无效。")
            self.send_json({"ok": True, **intake_document(self.knowledge_root, name, content)}, HTTPStatus.CREATED)
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            self.send_json({"ok": False, "error": str(exc)}, HTTPStatus.BAD_REQUEST)

    def serve_asset(self, request_path: str) -> None:
        requested = "index.html" if request_path in {"", "/"} else unquote(request_path).lstrip("/")
        path = (DASHBOARD_DIR / requested).resolve()
        if not path.is_file() or DASHBOARD_DIR not in path.parents and path != DASHBOARD_DIR:
            self.send_error(HTTPStatus.NOT_FOUND, "Asset not found")
            return
        content = path.read_bytes()
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", mimetypes.guess_type(str(path))[0] or "application/octet-stream")
        self.send_header("Content-Security-Policy", "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
        self.send_header("Content-Length", str(len(content)))
        self.end_headers()
        self.wfile.write(content)

    def send_json(self, value: dict[str, object], status: HTTPStatus = HTTPStatus.OK) -> None:
        body = json.dumps(value, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
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
