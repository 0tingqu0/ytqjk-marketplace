"""Locate the matching knowledge engine without a fixed plugin version."""

from __future__ import annotations

import importlib
import sys
from dataclasses import dataclass
from pathlib import Path
from types import ModuleType

from knowledge_engine_planner import (
    EngineNotConfigured,
    EnginePlanner,
    EngineProcessingError,
)


_MODULES = (
    "database",
    "intake_contracts",
    "intake_extraction_contracts",
    "document_intake_jobs",
    "document_intake_job_store",
    "artifact_safety",
    "image_ocr_backend",
    "image_input_guard",
    "image_ocr_secondary",
    "paddleocr_v3_backend",
    "paddle_structure_v3_backend",
    "image_document_extractor",
    "image_description_backend",
    "image_semantic_contract",
    "image_semantic_artifacts",
    "image_semantic_backend",
    "image_semantic_merge",
    "pdf_result_policy",
    "pdf_secondary_policy",
    "pdf_document_materializer",
    "pdf_document_extractor",
    "pdf_paddle_backend",
    "docling_content_order",
    "docling_layout_policy",
    "docling_error_mapping",
    "docling_picture_parser",
    "docling_picture_runtime",
    "docling_payload_parser",
    "docling_backend",
    "structured_document_chunks",
    "structured_document_intake",
    "intake_security",
)


class EngineLocationError(RuntimeError):
    """Engine is absent, ambiguous, or outside its plugin boundary."""


@dataclass(frozen=True, slots=True)
class KnowledgeEngine:
    scripts_root: Path
    modules: dict[str, ModuleType]

    def module(self, name: str) -> ModuleType:
        try:
            return self.modules[name]
        except KeyError as error:
            raise EngineLocationError("knowledge engine module is missing") \
                from error


def legacy_report(
    analysis: dict[str, object],
    purpose: str,
    source_path: str,
    details: dict[str, str],
    original_path: str,
    assessment: dict[str, object],
) -> str:
    report = (
        f"# 投递候选：{analysis['title']}\n\n## 入库分析\n\n"
        f"- 格式：`{analysis['format']}`\n"
        f"- 大小：{analysis['bytes']} bytes\n"
        f"- 行数：{analysis['lines']}\n- 摘要：{analysis['summary']}\n"
    )
    if purpose:
        report += f"- 作用：{purpose}\n"
    if source_path:
        report += f"- 文件夹位置：`{source_path}`\n"
    if "dimensions" in details:
        report += f"- 图片尺寸：{details['dimensions']}\n"
    if "audio" in details:
        report += f"- 音频信息：{details['audio']}\n"
    report += (
        f"- 原件：`{original_path}`\n"
        "此资料尚未验证或批准，仅作为候选知识供后续审阅。\n\n"
        f"## 批准评估\n\n- 结论：`{assessment['decision']}`\n"
    )
    return report + "".join(
        f"- {reason}\n" for reason in assessment["reasons"]
    ) + "\n## 原始资料\n\n"


def locate_knowledge_engine(plugin_root: Path) -> KnowledgeEngine:
    root = _directory(plugin_root, "orchestrator plugin")
    candidates = (
        root.parent / "ytqjk-knowledge" / "skills"
        / "ytqjk-knowledge" / "scripts",
        root.parents[1] / "ytqjk-knowledge" / root.name / "skills"
        / "ytqjk-knowledge" / "scripts",
    )
    found = []
    for candidate in candidates:
        if candidate.is_dir() and candidate not in found:
            found.append(candidate)
    if not found:
        raise EngineLocationError("ytqjk knowledge engine is not installed")
    scripts = _validated_scripts(found[0])
    skill_root = scripts.parent
    _prepare_namespace(skill_root, scripts)
    loaded = {}
    for name in _MODULES:
        module = importlib.import_module(f"scripts.{name}")
        origin = Path(str(module.__file__)).resolve(strict=True)
        if origin.parent != scripts or origin.name != f"{name}.py":
            raise EngineLocationError("knowledge engine module origin mismatch")
        loaded[name] = module
    return KnowledgeEngine(scripts, loaded)


def _validated_scripts(value: Path) -> Path:
    scripts = _directory(value, "knowledge engine scripts")
    for name in _MODULES:
        path = scripts / f"{name}.py"
        if not path.is_file() or _is_link(path):
            raise EngineLocationError(
                "knowledge engine is incomplete or linked"
            )
        if path.resolve(strict=True).parent != scripts:
            raise EngineLocationError("knowledge engine file escaped its root")
    current = scripts
    for _ in range(4):
        if _is_link(current):
            raise EngineLocationError("linked knowledge engine is blocked")
        current = current.parent
    return scripts


def _prepare_namespace(skill_root: Path, scripts: Path) -> None:
    loaded = sys.modules.get("scripts")
    if loaded is not None:
        paths = getattr(loaded, "__path__", ())
        resolved = {
            Path(str(path)).resolve()
            for path in paths
        }
        if scripts not in resolved:
            raise EngineLocationError(
                "another scripts package is already loaded"
            )
    skill = str(skill_root)
    if skill not in sys.path:
        sys.path.insert(0, skill)
    importlib.invalidate_caches()


def _directory(value: Path, label: str) -> Path:
    if not isinstance(value, Path):
        raise EngineLocationError(f"{label} path is invalid")
    try:
        resolved = value.resolve(strict=True)
    except (OSError, RuntimeError) as error:
        raise EngineLocationError(f"{label} is unavailable") from error
    if not resolved.is_dir() or _is_link(value):
        raise EngineLocationError(f"{label} is unsafe")
    return resolved


def _is_link(path: Path) -> bool:
    try:
        junction = getattr(path, "is_junction", lambda: False)()
        return path.is_symlink() or junction
    except OSError:
        return True


__all__ = [
    "EngineNotConfigured",
    "EnginePlanner",
    "EngineProcessingError",
    "EngineLocationError",
    "KnowledgeEngine",
    "legacy_report",
    "locate_knowledge_engine",
]
