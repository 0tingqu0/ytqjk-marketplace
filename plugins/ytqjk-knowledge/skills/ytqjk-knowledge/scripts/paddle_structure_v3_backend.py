"""Strict local PP-StructureV3 adapter for complex PDF pages."""

from __future__ import annotations

import hashlib
import importlib.metadata
import json
import math
import time
from collections.abc import Mapping
from dataclasses import dataclass
from io import BytesIO
from pathlib import Path
from typing import Callable

from PIL import Image

from scripts.artifact_safety import (
    ArtifactSafetyError,
    TreeGuard,
    snapshot_tree,
    verify_tree,
)

from scripts.image_input_guard import validate_image_input
from scripts.image_ocr_backend import OcrBackendError, OcrNotConfigured
from scripts.intake_extraction_contracts import RecognitionEvidence


EXPECTED_VERSION = "3.7.0"
MODEL_KEYS = frozenset({
    "layout_detection_model_dir",
    "text_detection_model_dir",
    "text_recognition_model_dir",
    "table_classification_model_dir",
    "wired_table_structure_recognition_model_dir",
    "wireless_table_structure_recognition_model_dir",
    "wired_table_cells_detection_model_dir",
    "wireless_table_cells_detection_model_dir",
})


@dataclass(frozen=True)
class StructureBlock:
    text: str
    box: tuple[float, float, float, float]
    confidence: float


@dataclass(frozen=True)
class StructureResult:
    width: int
    height: int
    blocks: tuple[StructureBlock, ...]
    elapsed_ms: int
    evidence: RecognitionEvidence


class PaddleStructureV3Backend:
    def __init__(
        self,
        model_paths: Mapping[str, str | Path],
        *,
        pipeline_factory: Callable[..., object] | None = None,
        version_getter: Callable[[], str] | None = None,
    ) -> None:
        self._paths = {key: Path(value) for key, value in model_paths.items()}
        self._factory = pipeline_factory
        self._version_getter = version_getter or _version
        self._loaded: tuple[object, RecognitionEvidence] | None = None
        self._guards: tuple[TreeGuard, ...] = ()

    def analyze(self, image_bytes: bytes) -> StructureResult:
        width, height = validate_image_input(image_bytes)
        pipeline, evidence = self._load()
        self._verify_guards(False)
        with Image.open(BytesIO(image_bytes)) as image:
            image.load()
            rgb = image.convert("RGB")
        started = time.perf_counter()
        try:
            output = pipeline.predict(rgb)  # type: ignore[attr-defined]
            self._verify_guards(False)
            payload = _single_payload(output)
            blocks = _blocks(payload, width, height)
        except (OcrBackendError, OcrNotConfigured):
            raise
        except Exception as error:
            raise OcrBackendError("PP-StructureV3 inference failed") from error
        elapsed = round((time.perf_counter() - started) * 1000)
        return StructureResult(width, height, blocks, elapsed, evidence)

    def _load(self) -> tuple[object, RecognitionEvidence]:
        if self._loaded is not None:
            self._verify_guards(True)
            return self._loaded
        paths, model_digest, guards = _validated_models(self._paths)
        version = self._version_getter()
        if version != EXPECTED_VERSION:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: paddleocr 3.7.0 is required"
            )
        factory = self._factory
        if factory is None:
            try:
                from paddleocr import PPStructureV3
            except (ImportError, ModuleNotFoundError) as error:
                raise OcrNotConfigured(
                    "NOT_CONFIGURED: PP-StructureV3 is unavailable"
                ) from error
            factory = PPStructureV3
        params: dict[str, object] = {
            **{key: str(value) for key, value in paths.items()},
            "device": "cpu",
            "enable_hpi": False,
            "enable_mkldnn": False,
            "precision": "fp32",
            "use_doc_orientation_classify": False,
            "use_doc_unwarping": False,
            "use_textline_orientation": False,
            "use_formula_recognition": False,
            "use_region_detection": False,
            "use_seal_recognition": False,
            "use_chart_recognition": False,
        }
        try:
            pipeline = factory(**params)
        except Exception as error:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: PP-StructureV3 models cannot load"
            ) from error
        if not callable(getattr(pipeline, "predict", None)):
            raise OcrBackendError("PP-StructureV3 pipeline is invalid")
        try:
            for guard in guards:
                verify_tree(guard)
        except ArtifactSafetyError as error:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: PP-StructureV3 models changed while loading"
            ) from error
        config = _digest({
            "models": model_digest,
            "params": {
                key: value
                for key, value in params.items()
                if key in {"device", "precision"}
                or key.startswith(("enable_", "use_"))
            },
            "version": version,
        })
        evidence = RecognitionEvidence(
            "pp-structure-v3",
            f"paddleocr-{version}:sha256:{model_digest}",
            config,
        )
        self._loaded = pipeline, evidence
        self._guards = guards
        return self._loaded

    def _verify_guards(self, loading: bool) -> None:
        try:
            for guard in self._guards:
                verify_tree(guard)
        except ArtifactSafetyError as error:
            if loading:
                raise OcrNotConfigured(
                    "NOT_CONFIGURED: PP-StructureV3 models changed"
                ) from error
            raise OcrBackendError(
                "PP-StructureV3 models changed during inference"
            ) from error


def _single_payload(output: object) -> dict[str, object]:
    try:
        items = list(output)  # type: ignore[arg-type]
    except TypeError as error:
        raise OcrBackendError("PP-StructureV3 output is invalid") from error
    if len(items) != 1:
        raise OcrBackendError("PP-StructureV3 must return one page")
    payload = getattr(items[0], "json", None)
    if type(payload) is not dict or type(payload.get("res")) is not dict:
        raise OcrBackendError("PP-StructureV3 result json is invalid")
    return payload["res"]  # type: ignore[return-value]


def _blocks(
    payload: dict[str, object], width: int, height: int,
) -> tuple[StructureBlock, ...]:
    values = payload.get("parsing_res_list")
    if type(values) is not list:
        raise OcrBackendError("PP-StructureV3 parsing result is missing")
    blocks = []
    for value in values:
        if type(value) is not dict:
            raise OcrBackendError("PP-StructureV3 block is invalid")
        text = value.get("block_content")
        if not isinstance(text, str) or not text.strip():
            continue
        box = _box(value.get("block_bbox"), width, height)
        score = _score(value.get("block_score"))
        blocks.append(StructureBlock(text.strip(), box, score))
    if not blocks:
        raise OcrBackendError("PP-StructureV3 returned no usable blocks")
    return tuple(blocks)


def _box(
    value: object, width: int, height: int,
) -> tuple[float, float, float, float]:
    if type(value) not in (list, tuple) or len(value) != 4:
        raise OcrBackendError("PP-StructureV3 box is invalid")
    if any(isinstance(item, bool) for item in value):
        raise OcrBackendError("PP-StructureV3 box is invalid")
    try:
        left, top, right, bottom = (float(item) for item in value)
    except (TypeError, ValueError) as error:
        raise OcrBackendError("PP-StructureV3 box is invalid") from error
    values = (left, top, right, bottom)
    valid = all(math.isfinite(item) for item in values)
    valid = valid and 0 <= left < right <= width
    valid = valid and 0 <= top < bottom <= height
    if not valid:
        raise OcrBackendError("PP-StructureV3 box is invalid")
    return left, top, right - left, bottom - top


def _score(value: object) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise OcrBackendError("PP-StructureV3 score is invalid")
    score = float(value)
    if not math.isfinite(score) or not 0 <= score <= 1:
        raise OcrBackendError("PP-StructureV3 score is invalid")
    return score


def _validated_models(
    paths: Mapping[str, Path],
) -> tuple[dict[str, Path], str, tuple[TreeGuard, ...]]:
    if set(paths) != MODEL_KEYS:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: complete PP-StructureV3 models are required"
        )
    resolved = {}
    inventory = {}
    guards = []
    for key, raw in sorted(paths.items()):
        try:
            guard = snapshot_tree(raw)
        except ArtifactSafetyError as error:
            raise OcrNotConfigured(
                f"NOT_CONFIGURED: PP-StructureV3 {key} is unsafe"
            ) from error
        if not guard.files:
            raise OcrNotConfigured(
                f"NOT_CONFIGURED: PP-StructureV3 {key} is invalid"
            )
        root = guard.root
        resolved[key] = root
        inventory[key] = guard.hashes
        guards.append(guard)
    return resolved, _digest(inventory), tuple(guards)


def _digest(value: object) -> str:
    data = json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(data).hexdigest()


def _version() -> str:
    try:
        return importlib.metadata.version("paddleocr")
    except importlib.metadata.PackageNotFoundError as error:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: paddleocr is unavailable"
        ) from error


__all__ = [
    "MODEL_KEYS",
    "PaddleStructureV3Backend",
    "StructureBlock",
    "StructureResult",
]
