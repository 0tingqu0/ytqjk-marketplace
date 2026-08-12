from __future__ import annotations

import argparse
import base64
import binascii
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

DASHBOARD_DIR = Path(__file__).resolve().parent; SCRIPTS_DIR = DASHBOARD_DIR.parent / "scripts"
sys.path.insert(0, str(SCRIPTS_DIR))
sys.path.insert(0, str(DASHBOARD_DIR))

from approval_assessment import assess_for_approval  # noqa: E402
from approval_promotion import promote, promote_eligible  # noqa: E402
from candidate_actions import candidate_document, delete_candidate, update_candidate  # noqa: E402
from dashboard_snapshot import snapshot as build_snapshot  # noqa: E402
from platform_paths import default_knowledge_root  # noqa: E402
from rag_security import contains_high_confidence_secret, is_sensitive_path  # noqa: E402
from intake_formats import (  # noqa: E402
    SUPPORTED_EXTENSIONS, TEXT_EXTENSIONS, TEXT_FILE_NAMES, extract_upload, supported_extension,
)
from knowledge_dedup import find_duplicate  # noqa: E402
from knowledge_chunks import write_chunks  # noqa: E402


MAX_PREVIEW_CHARS = 24_000; MAX_INTAKE_BYTES = 10 * 1024 * 1024
INTAKE_DIR = "personal-experience/candidates/imports"
SAFE_FILE_NAME = re.compile(r"[\w .()（）-]+\.[A-Za-z0-9]+", re.UNICODE)
def safe_document(root: Path, raw_path: str) -> Path | None:
    candidate = (root / raw_path).resolve()
    try:
        candidate.relative_to(root.resolve())
    except ValueError:
        return None
    return candidate if candidate.suffix == ".md" and candidate.is_file() else None


def snapshot(root: Path) -> dict[str, object]:
    return build_snapshot(root, safe_document)


def analyze_intake(name: str, content: str, source_bytes: int) -> dict[str, object]:
    lines = [line.strip() for line in content.splitlines() if line.strip()]
    headings = [line.lstrip("#").strip() for line in lines if line.startswith("#")]
    summary = next((line for line in lines if not line.startswith("---")), "")
    return {
        "format": Path(name).suffix.lower().lstrip("."),
        "bytes": source_bytes,
        "lines": len(content.splitlines()),
        "title": headings[0] if headings else Path(name).stem,
        "summary": summary[:240],
    }


def intake_document(root: Path, name: str, content: str, purpose: str = "") -> dict[str, str]:
    return intake_upload(root, name, content.encode("utf-8"), purpose)


def intake_upload(root: Path, name: str, source: bytes, purpose: str = "") -> dict[str, str]:
    source_name = Path(name).name.strip()
    if (
        not source_name
        or source_name != name
        or (SAFE_FILE_NAME.fullmatch(source_name) is None and source_name.lower() not in TEXT_FILE_NAMES)
        or supported_extension(source_name) not in SUPPORTED_EXTENSIONS
    ):
        raise ValueError("仅支持文本、.docx、.pptx、.xlsx 和常见图片资料。")
    if is_sensitive_path(source_name):
        raise ValueError("资料可能包含凭据或敏感文件名，未保存。")
    if not source or len(source) > MAX_INTAKE_BYTES:
        raise ValueError("单份资料必须非空且不能超过 10 MiB。")
    content, details = extract_upload(source_name, source)
    normalized_purpose = validate_purpose(purpose)
    if "\x00" in content or contains_high_confidence_secret(content):
        raise ValueError("资料可能包含凭据或敏感内容，未保存。")
    if supported_extension(source_name) in TEXT_EXTENSIONS and not content.strip():
        raise ValueError("文本资料必须非空。")
    duplicate = find_duplicate(root, content, source)
    if duplicate is not None:
        raise ValueError(f"发现相同知识，未重复入库：{duplicate}")
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    identifier = f"{timestamp}-{uuid.uuid4().hex[:8]}-{Path(source_name).stem}"
    target = root / INTAKE_DIR / f"{identifier}.md"
    original = target.parent / "originals" / f"{identifier}-{source_name}"
    target.parent.mkdir(parents=True, exist_ok=True)
    original.parent.mkdir(parents=True, exist_ok=True)
    analysis = analyze_intake(source_name, content, len(source))
    assessment = assess_for_approval(content, "dimensions" in details)
    metadata = (
        "---\nstatus: CANDIDATE\nsource: dashboard-intake\n"
        f"intake_id: {identifier}\noriginal_name: {source_name}\noriginal_path: {original.relative_to(root).as_posix()}\nreceived_at: {datetime.now(timezone.utc).isoformat()}\n---\n\n"
    )
    report = (
        f"# 投递候选：{analysis['title']}\n\n## 入库分析\n\n"
        f"- 格式：`{analysis['format']}`\n- 大小：{analysis['bytes']} bytes\n"
        f"- 行数：{analysis['lines']}\n- 摘要：{analysis['summary']}\n"
        + (f"- 作用：{normalized_purpose}\n" if normalized_purpose else "")
        + (f"- 图片尺寸：{details['dimensions']}\n" if "dimensions" in details else "")
        + f"- 原件：`{original.relative_to(root).as_posix()}`\n"
        "此资料尚未验证或批准，仅作为候选知识供后续审阅。\n\n"
        "## 批准评估\n\n"
        f"- 结论：`{assessment['decision']}`\n"
        + "".join(f"- {reason}\n" for reason in assessment["reasons"])
        + "\n## 原始资料\n\n"
    )
    original.write_bytes(source)
    chunks = write_chunks(root, identifier, source_name, content)
    report = report.replace("- 原件：", f"- 知识片段：{len(chunks)} 个\n- 原件：")
    target.write_text(metadata + report + (content or "图片未进行文字识别，已记录文件元数据。"), encoding="utf-8")
    promoted = promote_eligible(root)
    path = target.relative_to(root).as_posix()
    return {
        "path": path.replace("/candidates/", "/approved/") if path in promoted else path,
        "state": "approved" if path in promoted else "candidate",
        "assessment": assessment,
        "chunks": len(chunks),
    }


def validate_purpose(purpose: str) -> str:
    normalized = purpose.strip()
    if len(normalized) > 500 or "\x00" in normalized or contains_high_confidence_secret(normalized):
        raise ValueError("资料作用无效或可能包含敏感内容。")
    return normalized


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
            relative = path.relative_to(self.knowledge_root).as_posix()
            content = path.read_text(encoding="utf-8")
            self.send_json({"path": relative, "content": content if candidate_document(self.knowledge_root, relative) else content[:MAX_PREVIEW_CHARS]})
            return
        self.serve_asset(url.path)

    def do_PUT(self) -> None:  # noqa: N802 - inherited API name
        if urlparse(self.path).path != "/api/candidate":
            self.send_error(HTTPStatus.NOT_FOUND, "API not found")
            return
        try:
            payload = self.read_payload()
            path, content = payload.get("path"), payload.get("content")
            if not isinstance(path, str) or not isinstance(content, str):
                raise ValueError("候选资料路径或内容无效。")
            result = update_candidate(self.knowledge_root, path, content)
            result["assessment"] = assess_for_approval(content, False)
            promoted = promote_eligible(self.knowledge_root)
            if result["path"] in promoted:
                result["path"] = result["path"].replace("/candidates/", "/approved/")
                result["state"] = "approved"
            self.send_json({"ok": True, **result})
        except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            self.send_json({"ok": False, "error": str(exc)}, HTTPStatus.BAD_REQUEST)

    def do_DELETE(self) -> None:  # noqa: N802 - inherited API name
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
            self.send_json({"ok": False, "error": str(exc)}, HTTPStatus.BAD_REQUEST)

    def do_POST(self) -> None:  # noqa: N802 - inherited API name
        if urlparse(self.path).path == "/api/candidate/approve":
            try:
                payload = self.read_payload()
                raw_path = payload.get("path")
                if not isinstance(raw_path, str):
                    raise ValueError("候选资料路径无效。")
                candidate = candidate_document(self.knowledge_root, raw_path)
                if candidate is None or not promote(self.knowledge_root, candidate, require_ready=False):
                    raise ValueError("候选资料无效或包含敏感内容，不能批准。")
                self.send_json({"ok": True, "path": raw_path.replace("/candidates/", "/approved/"), "state": "approved"})
            except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
                self.send_json({"ok": False, "error": str(exc)}, HTTPStatus.BAD_REQUEST)
            return
        if urlparse(self.path).path != "/api/intake":
            self.send_error(HTTPStatus.NOT_FOUND, "API not found")
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            self.send_json({"ok": False, "error": "请求长度无效。"}, HTTPStatus.BAD_REQUEST)
            return
        if not 0 < length <= (MAX_INTAKE_BYTES * 4 // 3) + 8192:
            self.send_error(HTTPStatus.REQUEST_ENTITY_TOO_LARGE, "Payload too large")
            return
        try:
            payload = self.read_payload(length)
            name, content, purpose = payload.get("name"), payload.get("content"), payload.get("purpose", "")
            if not isinstance(name, str) or not isinstance(content, str) or not isinstance(purpose, str):
                raise ValueError("资料名称或内容无效。")
            if payload.get("encoding") == "base64":
                source = base64.b64decode(content, validate=True)
                result = intake_upload(self.knowledge_root, name, source, purpose)
            else:
                result = intake_document(self.knowledge_root, name, content, purpose)
            self.send_json({"ok": True, **result}, HTTPStatus.CREATED)
        except (binascii.Error, UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
            self.send_json({"ok": False, "error": str(exc)}, HTTPStatus.BAD_REQUEST)

    def read_payload(self, length: int | None = None) -> dict[str, object]:
        content_length = length if length is not None else int(self.headers.get("Content-Length", "0"))
        if not 0 < content_length <= (MAX_INTAKE_BYTES * 4 // 3) + 8192:
            raise ValueError("请求长度无效。")
        payload = json.loads(self.rfile.read(content_length).decode("utf-8"))
        if not isinstance(payload, dict):
            raise ValueError("请求格式错误。")
        return payload

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
