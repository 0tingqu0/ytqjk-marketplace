from __future__ import annotations

from pathlib import Path

from approval_assessment import assess_for_approval
from candidate_actions import (
    candidate_document,
    candidate_version,
    update_candidate,
)
from dashboard_documents import read_document


def document_payload(
    root: Path,
    raw_path: str,
    max_preview_chars: int,
) -> dict[str, object] | None:
    document = read_document(root, raw_path)
    if document is None:
        return None
    path, content = document
    relative = path.relative_to(root).as_posix()
    editable = candidate_document(root, relative) is not None
    return {
        "path": relative,
        "version": candidate_version(content),
        "content": content if editable else content[:max_preview_chars],
    }


def update_payload(root: Path, payload: dict[str, object]) -> dict[str, object]:
    path = payload.get("path")
    content = payload.get("content")
    version = payload.get("expected_version")
    if not all(isinstance(item, str) for item in (path, content, version)):
        raise ValueError("候选资料路径或内容无效。")
    result = update_candidate(root, path, content, version)
    result["assessment"] = assess_for_approval(content, False)
    return {"ok": True, **result}
