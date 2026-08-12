from __future__ import annotations

import re
from datetime import datetime, timezone
from pathlib import Path

from approval_assessment import assess_for_approval
from rag_security import contains_high_confidence_secret


CANDIDATE_ROOTS = ("personal-experience/candidates", "error-experience/candidates")


def promote_eligible(root: Path) -> list[str]:
    promoted = []
    for relative in CANDIDATE_ROOTS:
        source_root = root / relative
        if not source_root.is_dir():
            continue
        documents = [*source_root.glob("*.md"), *(source_root / "imports").glob("*.md")]
        for path in documents:
            if promote(root, path):
                promoted.append(path.relative_to(root).as_posix())
    return promoted


def promote(root: Path, path: Path) -> bool:
    try:
        content = path.read_text(encoding="utf-8")
    except OSError:
        return False
    if contains_high_confidence_secret(content):
        return False
    assessment = assess_for_approval(assessment_content(content), False)
    if assessment["decision"] != "READY_FOR_REVIEW":
        return False
    relative = path.relative_to(root).as_posix()
    target = root / relative.replace("/candidates/", "/approved/", 1)
    target.parent.mkdir(parents=True, exist_ok=True)
    approved_at = datetime.now(timezone.utc).isoformat()
    approved = re.sub(r"(?m)^status: CANDIDATE$", "status: APPROVED", content, count=1)
    if approved == content:
        approved = f"---\nstatus: APPROVED\napproved_at: {approved_at}\n---\n\n{content}"
    else:
        approved = approved.replace("\n---", f"\napproved_at: {approved_at}\napproval: automatic-evidence-gate\n---", 1)
    target.write_text(approved, encoding="utf-8")
    path.unlink()
    return True


def assessment_content(content: str) -> str:
    marker = "## 原始资料\n\n"
    return content.partition(marker)[2] if marker in content else content
