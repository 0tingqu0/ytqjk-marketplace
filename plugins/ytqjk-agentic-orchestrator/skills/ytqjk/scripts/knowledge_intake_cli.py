from __future__ import annotations

import argparse
import hashlib
import json
import re
from datetime import datetime, timezone
from pathlib import Path

from platform_paths import default_knowledge_root
from project_tracking import identify_project, track_project
from rag_security import contains_high_confidence_secret
from session_memory import ensure_anchor, validate_session_binding


MAX_CONTENT_CHARS = 24_000
KNOWLEDGE_DIRS = (
    "verified",
    "personal-experience/approved",
    "personal-experience/candidates",
    "error-experience/approved",
    "error-experience/candidates",
)


def submit_candidate(
    root: Path,
    project_root: Path,
    session_id: str,
    query: str,
    content: str,
    sources: list[str],
) -> dict[str, object]:
    project = identify_project(project_root)
    validate_session_binding(root, session_id, project["id"])
    track_project(root, project_root, project)
    ensure_anchor(root, session_id, project["id"])
    normalized = content.strip()
    if not normalized or len(normalized) > MAX_CONTENT_CHARS or "\x00" in normalized:
        raise ValueError("检索知识必须非空且不超过 24000 字符。")
    if contains_high_confidence_secret(normalized):
        raise ValueError("检索知识可能包含敏感信息，未保存。")
    safe_sources = [source.strip() for source in sources if source.strip()]
    if not safe_sources or any(len(source) > 500 or contains_high_confidence_secret(source) for source in safe_sources):
        raise ValueError("至少提供一个安全、可追溯的来源。")
    duplicate = find_duplicate(root, normalized)
    if duplicate:
        return {"ok": True, "state": "DUPLICATE", "path": duplicate, "project_id": project["id"]}
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    digest = hashlib.sha256(normalized.encode("utf-8")).hexdigest()[:10]
    target = root / "personal-experience" / "candidates" / f"{stamp}-research-{digest}.md"
    target.parent.mkdir(parents=True, exist_ok=True)
    source_rows = "\n".join(f"- {source}" for source in safe_sources)
    target.write_text(
        "---\nstatus: CANDIDATE\nsource: external-research\n"
        f"project_id: {project['id']}\nquery: {json.dumps(query, ensure_ascii=False)}\n"
        f"received_at: {datetime.now(timezone.utc).isoformat()}\n---\n\n"
        f"# 外部检索候选知识\n\n## 来源\n\n{source_rows}\n\n## 检索结果\n\n{normalized}\n",
        encoding="utf-8",
    )
    return {
        "ok": True,
        "state": "CANDIDATE",
        "path": target.relative_to(root).as_posix(),
        "project_id": project["id"],
    }


def find_duplicate(root: Path, content: str) -> str | None:
    digest = _digest(content)
    for relative in KNOWLEDGE_DIRS:
        directory = root / relative
        if not directory.is_dir():
            continue
        for path in directory.rglob("*.md"):
            try:
                existing = path.read_text(encoding="utf-8")
            except OSError:
                continue
            if _digest(existing.partition("## 检索结果\n\n")[2] or existing) == digest:
                return path.relative_to(root).as_posix()
    return None


def _digest(content: str) -> bytes:
    normalized = re.sub(r"\s+", " ", content).strip()
    return hashlib.sha256(normalized.encode("utf-8")).digest()


def main() -> int:
    parser = argparse.ArgumentParser(description="Submit externally researched knowledge as a global candidate.")
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    parser.add_argument("--project-root", type=Path, required=True)
    parser.add_argument("--session-id", required=True)
    parser.add_argument("--query", required=True)
    parser.add_argument("--content-file", type=Path, required=True)
    parser.add_argument("--source", action="append", required=True)
    args = parser.parse_args()
    try:
        result = submit_candidate(
            args.knowledge_root.resolve(), args.project_root.resolve(), args.session_id,
            args.query, args.content_file.read_text(encoding="utf-8"), args.source,
        )
    except (OSError, RuntimeError, ValueError) as exc:
        result = {"ok": False, "error": str(exc)}
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 0 if result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
