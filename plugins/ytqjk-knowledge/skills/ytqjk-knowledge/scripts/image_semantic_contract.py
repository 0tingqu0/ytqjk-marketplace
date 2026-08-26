"""Stable labels and public result mapping for image semantics."""

from __future__ import annotations

from collections.abc import Sequence

from scripts.intake_extraction_contracts import (
    ImageClassification,
    RecognitionEvidence,
)


MODEL_NAME = "DocumentFigureClassifier-v2.5"
ENGINE_NAME = "docling-picture-classifier-onnx"
DESCRIPTION_FAILED_TAG = "description-failed"
EXPECTED_LABELS = (
    "logo",
    "photograph",
    "icon",
    "engineering_drawing",
    "line_chart",
    "bar_chart",
    "other",
    "table",
    "flow_chart",
    "screenshot_from_computer",
    "signature",
    "screenshot_from_manual",
    "geographical_map",
    "pie_chart",
    "page_thumbnail",
    "stamp",
    "music",
    "calendar",
    "qr_code",
    "bar_code",
    "full_page_image",
    "scatter_plot",
    "chemistry_structure",
    "topographical_map",
    "crossword_puzzle",
    "box_plot",
)
_SCREENSHOTS = frozenset((
    "screenshot_from_computer",
    "screenshot_from_manual",
))
_DIAGRAMS = frozenset((
    "engineering_drawing",
    "line_chart",
    "bar_chart",
    "flow_chart",
    "geographical_map",
    "pie_chart",
    "scatter_plot",
    "chemistry_structure",
    "topographical_map",
    "box_plot",
))
_DOCUMENTS = frozenset((
    "logo",
    "icon",
    "signature",
    "page_thumbnail",
    "stamp",
    "music",
    "calendar",
    "qr_code",
    "bar_code",
    "full_page_image",
    "crossword_puzzle",
))
_ZH_LABELS = {
    "photograph": "照片",
    "table": "表格",
    "flow_chart": "流程图",
    "engineering_drawing": "工程图",
    "screenshot_from_computer": "电脑截图",
    "screenshot_from_manual": "手册截图",
    "geographical_map": "地图",
    "topographical_map": "地形图",
    "line_chart": "折线图",
    "bar_chart": "柱状图",
    "pie_chart": "饼图",
    "scatter_plot": "散点图",
    "box_plot": "箱线图",
    "chemistry_structure": "化学结构图",
}


def _category(label: str) -> str:
    if label == "photograph":
        return "photo"
    if label == "table":
        return "table"
    if label in _SCREENSHOTS:
        return "screenshot"
    if label in _DIAGRAMS:
        return "diagram"
    if label in _DOCUMENTS:
        return "document"
    return "unknown"


def build_classification(
    labels: tuple[str, ...],
    scores: Sequence[float],
    order: Sequence[int],
    evidence: RecognitionEvidence,
) -> ImageClassification:
    confidence = float(scores[order[0]])
    if confidence < 0.5:
        return ImageClassification(
            "unknown",
            ("unknown", "other", "semantic"),
            "视觉模型无法可靠判断图片内容，已转人工复审。",
            confidence,
            evidence,
        )
    selected = tuple(labels[index] for index in order[:3])
    category = _category(selected[0])
    names = [
        _ZH_LABELS.get(label, label.replace("_", " "))
        for label in selected
    ]
    summary = (
        f"视觉内容识别为{names[0]}，置信度 {confidence:.3f}；"
        f"次级候选：{'、'.join(names[1:])}。"
    )
    tags = tuple(dict.fromkeys((category, *selected, "semantic")))
    return ImageClassification(
        category,
        tags,
        summary,
        confidence,
        evidence,
    )


__all__ = [
    "DESCRIPTION_FAILED_TAG",
    "ENGINE_NAME",
    "EXPECTED_LABELS",
    "MODEL_NAME",
    "build_classification",
]
