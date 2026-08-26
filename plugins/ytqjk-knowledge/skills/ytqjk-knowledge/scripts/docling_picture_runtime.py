"""Offline Docling picture-description configuration and evidence."""

from __future__ import annotations

import hashlib
import json
from collections.abc import Mapping
from pathlib import Path
from types import ModuleType

from scripts.image_description_backend import PROMPT
from scripts.intake_extraction_contracts import RecognitionEvidence
from scripts.pdf_document_extractor import PdfExtractionError


SMOLVLM_REPO = "HuggingFaceTB/SmolVLM-256M-Instruct"
SMOLVLM_CACHE = "HuggingFaceTB--SmolVLM-256M-Instruct"
SMOLVLM_VERSION = "SmolVLM-256M-Instruct"


def _digest(value: object) -> str:
    encoded = json.dumps(
        value,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def validate_picture_model(
    root: Path,
    configured: Path | None,
) -> bool:
    if configured is None:
        return False
    try:
        actual = configured.resolve(strict=True)
        expected = (root / SMOLVLM_CACHE).resolve(strict=True)
        actual.relative_to(root)
    except (OSError, RuntimeError, ValueError) as error:
        raise PdfExtractionError(
            "NOT_CONFIGURED",
            "local Docling picture model is unavailable",
        ) from error
    required = (actual / "config.json", actual / "model.safetensors")
    if actual != expected or not all(path.is_file() for path in required):
        raise PdfExtractionError(
            "NOT_CONFIGURED",
            "local Docling picture model is invalid",
        )
    return True


def configure_picture(
    options: ModuleType,
    keywords: dict[str, object],
    enabled: bool,
) -> None:
    if not enabled:
        return
    picture = options.PictureDescriptionVlmOptions(
        repo_id=SMOLVLM_REPO,
        prompt=PROMPT,
        generation_config={
            "do_sample": False,
            "max_new_tokens": 160,
        },
        batch_size=1,
        scale=1.0,
        picture_area_threshold=0.01,
    )
    keywords.update({
        "do_picture_description": True,
        "generate_picture_images": True,
        "images_scale": 1.0,
        "picture_description_options": picture,
    })


def docling_config_digest(
    *,
    docling_version: str,
    rapidocr_version: str,
    ocr: bool,
    picture: bool,
    timeout: int,
    tree_digest: str,
    rapid_paths: Mapping[str, str],
) -> str:
    return _digest({
        "docling_version": docling_version,
        "external_plugins": False,
        "layout_engine": "onnxruntime",
        "model_tree_digest": tree_digest,
        "ocr": ocr,
        "picture_description": picture,
        "rapidocr": {
            "backend": "onnxruntime",
            "force_full_page_ocr": ocr,
            "paths": dict(sorted(rapid_paths.items())),
            "text_score": 0.5,
            "version": rapidocr_version,
        },
        "remote_services": False,
        "table_structure": True,
        "timeout": timeout,
    })


def picture_evidence(
    tree_digest: str,
    config_digest: str,
) -> RecognitionEvidence:
    return RecognitionEvidence(
        "docling-picture-description",
        f"{SMOLVLM_VERSION}@sha256:{tree_digest}",
        config_digest,
    )


__all__ = [
    "SMOLVLM_CACHE",
    "configure_picture",
    "docling_config_digest",
    "picture_evidence",
    "validate_picture_model",
]
