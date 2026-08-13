from __future__ import annotations

import re


MIN_APPROVAL_CHARS = 200
EVIDENCE_PATTERN = re.compile(
    r"https?://|\b(?:commit|sha|test|tested|source|evidence)\b|来源|证据|测试|提交|版本|复现",
    re.IGNORECASE,
)


def assess_for_approval(content: str, is_image: bool) -> dict[str, object]:
    reasons = []
    if is_image:
        reasons.append("图片未进行文字识别，需补充说明和来源")
    elif len(content.strip()) < MIN_APPROVAL_CHARS:
        reasons.append(f"有效文本不足 {MIN_APPROVAL_CHARS} 字符")
    if not EVIDENCE_PATTERN.search(content):
        reasons.append("缺少可追溯的来源、证据或验证线索")
    return {
        "decision": "READY_FOR_REVIEW" if not reasons else "NOT_READY",
        "reasons": reasons or ["满足完整性与可追溯性要求，可进入人工复审"],
    }
