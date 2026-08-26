"""Construct verified local vision components for EnginePlanner."""

from __future__ import annotations

from types import ModuleType

from knowledge_engine_models import (
    ModelManifestError,
    ModelSettings,
    picture_classifier_paths,
)


class VisionNotConfigured(RuntimeError):
    pass


def build_vision(
    modules: dict[str, ModuleType],
    settings: ModelSettings | None,
) -> tuple[object, object | None, object | None]:
    primary_module = modules["image_ocr_backend"]
    if settings is None:
        return primary_module.RapidOcrBackend({}), None, None
    try:
        picture = modules["image_semantic_backend"]
        classifier_paths = picture_classifier_paths(
            settings.root,
            settings.files,
        )
        classifier = picture.OnnxImageSemanticClassifier(classifier_paths)
        description = modules["image_description_backend"]
        describer = description.SmolVlmDescriber(settings.smolvlm)
        merger = modules["image_semantic_merge"]
        semantic = merger.MergedImageSemanticClassifier(
            classifier,
            describer,
        )
        paddle = modules["paddleocr_v3_backend"]
        secondary = paddle.PaddleOcrV3Backend(
            settings.paddleocr,
            params={
                "device": "cpu",
                "enable_hpi": False,
                "enable_mkldnn": False,
                "ocr_version": "PP-OCRv6",
                "precision": "fp32",
                "use_doc_orientation_classify": False,
                "use_doc_unwarping": False,
                "use_textline_orientation": False,
            },
        )
    except (KeyError, AttributeError, ModelManifestError) as error:
        raise VisionNotConfigured(
            "VISION_RUNTIME_MANIFEST_INVALID"
        ) from error
    primary_paths = {
        key: settings.root / relative
        for key, relative in settings.rapidocr.items()
    }
    return primary_module.RapidOcrBackend(primary_paths), semantic, secondary


def build_pdf_secondary(
    modules: dict[str, ModuleType],
    settings: ModelSettings | None,
) -> object | None:
    if settings is None:
        return None
    try:
        paddle = modules["paddleocr_v3_backend"]
        ocr = paddle.PaddleOcrV3Backend(
            settings.paddleocr,
            params={
                "device": "cpu",
                "enable_hpi": False,
                "enable_mkldnn": False,
                "ocr_version": "PP-OCRv6",
                "precision": "fp32",
                "use_doc_orientation_classify": False,
                "use_doc_unwarping": False,
                "use_textline_orientation": False,
            },
        )
        structure = None
        if settings.ppstructure is not None:
            adapter = modules["paddle_structure_v3_backend"]
            structure = adapter.PaddleStructureV3Backend(
                settings.ppstructure
            )
        pdf = modules["pdf_paddle_backend"]
        return pdf.PaddlePdfSecondaryBackend(
            ocr,
            structure_backend=structure,
        )
    except (KeyError, AttributeError, ModelManifestError) as error:
        raise VisionNotConfigured(
            "VISION_RUNTIME_MANIFEST_INVALID"
        ) from error


__all__ = [
    "VisionNotConfigured",
    "build_pdf_secondary",
    "build_vision",
]
