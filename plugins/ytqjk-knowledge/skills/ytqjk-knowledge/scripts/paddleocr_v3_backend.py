from __future__ import annotations

import hashlib
import importlib.metadata
import json
import math
import time
from io import BytesIO
from pathlib import Path
from typing import Callable, Mapping

from scripts.artifact_safety import (
    ArtifactSafetyError,
    TreeGuard,
    snapshot_tree,
    verify_tree,
)

from scripts.image_ocr_backend import (
    OcrBackendError,
    OcrBackendResult,
    OcrEngineEvidence,
    OcrNotConfigured,
    OcrPoint,
    OcrTextBlock,
)


_MODEL_KEYS = frozenset(
    {"text_detection_model_dir", "text_recognition_model_dir"}
)
_PARAM_KEYS = frozenset(
    {
        "cpu_threads",
        "device",
        "enable_hpi",
        "enable_mkldnn",
        "lang",
        "ocr_version",
        "precision",
        "text_rec_score_thresh",
        "use_doc_orientation_classify",
        "use_doc_unwarping",
        "use_textline_orientation",
    }
)


class PaddleOcrV3Backend:
    """Local PaddleOCR 3.x adapter using documented predict/json fields."""

    def __init__(
        self,
        model_paths: Mapping[str, str | Path],
        *,
        params: Mapping[str, str | int | float | bool] | None = None,
        pipeline_factory: Callable[..., object] | None = None,
        image_decoder: Callable[[bytes], object] | None = None,
        version_getter: Callable[[], str] | None = None,
    ) -> None:
        self._model_paths = {
            key: Path(value) for key, value in model_paths.items()
        }
        self._params = dict(params or {})
        self._validate_params()
        self._pipeline_factory = pipeline_factory
        self._image_decoder = image_decoder or _decode_image
        self._version_getter = version_getter or _installed_version
        self._pipeline: object | None = None
        self._evidence: OcrEngineEvidence | None = None
        self._guards: tuple[TreeGuard, ...] = ()

    def recognize(self, image_bytes: bytes) -> OcrBackendResult:
        if not isinstance(image_bytes, bytes) or not image_bytes:
            raise OcrBackendError("image input must be non-empty bytes")
        pipeline, evidence = self._load_pipeline()
        self._verify_guards(False)
        try:
            image = self._image_decoder(image_bytes)
            height, width = _image_size(image)
            started = time.perf_counter()
            output = pipeline.predict(image)  # type: ignore[attr-defined]
            self._verify_guards(False)
            elapsed_ms = round((time.perf_counter() - started) * 1000)
            payload = _single_json(output)
            blocks = _blocks(payload)
        except (OcrBackendError, OcrNotConfigured):
            raise
        except Exception as exc:
            message = f"PaddleOCR inference failed: {type(exc).__name__}"
            raise OcrBackendError(message) from exc
        return OcrBackendResult(
            width,
            height,
            blocks,
            elapsed_ms,
            evidence,
        )

    def _load_pipeline(
        self,
    ) -> tuple[object, OcrEngineEvidence]:
        if self._pipeline is not None and self._evidence is not None:
            self._verify_guards(True)
            return self._pipeline, self._evidence
        paths, model_digest, guards = _validate_models(self._model_paths)
        version = self._version_getter()
        if not isinstance(version, str) or not version.startswith("3."):
            raise OcrNotConfigured(
                "NOT_CONFIGURED: paddleocr major version must be 3"
            )
        factory = self._pipeline_factory
        if factory is None:
            try:
                from paddleocr import PaddleOCR
            except (ImportError, ModuleNotFoundError) as exc:
                raise OcrNotConfigured(
                    "NOT_CONFIGURED: paddleocr 3.x is unavailable"
                ) from exc
            factory = PaddleOCR
        runtime = dict(self._params)
        runtime.update({key: str(value) for key, value in paths.items()})
        try:
            pipeline = factory(**runtime)
        except (ImportError, ModuleNotFoundError) as exc:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: PaddleOCR runtime is unavailable"
            ) from exc
        except Exception as exc:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: PaddleOCR models cannot load"
            ) from exc
        if not callable(getattr(pipeline, "predict", None)):
            raise OcrBackendError("PaddleOCR pipeline has no predict method")
        try:
            for guard in guards:
                verify_tree(guard)
        except ArtifactSafetyError as exc:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: PaddleOCR models changed while loading"
            ) from exc
        config = {
            "engine": "paddleocr-3",
            "model_digest": model_digest,
            "package_version": version,
            "params": self._params,
        }
        evidence = OcrEngineEvidence(
            "paddleocr-3",
            version,
            model_digest,
            _canonical_digest(config),
        )
        self._pipeline = pipeline
        self._evidence = evidence
        self._guards = guards
        return pipeline, evidence

    def _verify_guards(self, loading: bool) -> None:
        try:
            for guard in self._guards:
                verify_tree(guard)
        except ArtifactSafetyError as exc:
            if loading:
                raise OcrNotConfigured(
                    "NOT_CONFIGURED: PaddleOCR models changed"
                ) from exc
            raise OcrBackendError(
                "PaddleOCR models changed during inference"
            ) from exc

    def _validate_params(self) -> None:
        if not set(self._params).issubset(_PARAM_KEYS):
            raise ValueError("unsupported PaddleOCR parameter")
        for value in self._params.values():
            if not isinstance(value, (str, int, float, bool)):
                raise ValueError("PaddleOCR parameters must be scalar")
            if isinstance(value, float) and not math.isfinite(value):
                raise ValueError("PaddleOCR parameters must be finite")


def _installed_version() -> str:
    try:
        return importlib.metadata.version("paddleocr")
    except importlib.metadata.PackageNotFoundError as exc:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: paddleocr 3.x is not installed"
        ) from exc


def _decode_image(image_bytes: bytes) -> object:
    try:
        import numpy
        from PIL import Image
        with Image.open(BytesIO(image_bytes)) as image:
            image.load()
            return numpy.asarray(image.convert("RGB"))
    except (ImportError, ModuleNotFoundError) as exc:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: Pillow and numpy are required"
        ) from exc
    except Exception as exc:
        raise OcrBackendError("PaddleOCR image decode failed") from exc


def _image_size(image: object) -> tuple[int, int]:
    shape = getattr(image, "shape", None)
    if shape is None or len(shape) < 2:
        raise OcrBackendError("PaddleOCR input has no image dimensions")
    height = _positive_int("height", shape[0])
    width = _positive_int("width", shape[1])
    return height, width


def _single_json(output: object) -> dict[str, object]:
    try:
        items = list(output)  # type: ignore[arg-type]
    except TypeError as exc:
        raise OcrBackendError("PaddleOCR output is not iterable") from exc
    if len(items) != 1:
        raise OcrBackendError("PaddleOCR image must return one result")
    payload = getattr(items[0], "json", None)
    if type(payload) is not dict or type(payload.get("res")) is not dict:
        raise OcrBackendError("PaddleOCR result json is invalid")
    return payload["res"]  # type: ignore[return-value]


def _blocks(payload: dict[str, object]) -> tuple[OcrTextBlock, ...]:
    names = ("rec_texts", "rec_scores", "rec_polys")
    values = tuple(_sequence(payload.get(name), name) for name in names)
    if len({len(value) for value in values}) != 1:
        raise OcrBackendError("PaddleOCR result lengths do not match")
    blocks = []
    for text, score, polygon in zip(*values, strict=True):
        if not isinstance(text, str):
            raise OcrBackendError("PaddleOCR text must be text")
        if not text.strip():
            continue
        quad = _quad(polygon)
        try:
            blocks.append(OcrTextBlock(text, quad, score))
        except ValueError as exc:
            raise OcrBackendError(str(exc)) from exc
    return tuple(blocks)


def _quad(value: object) -> tuple[OcrPoint, OcrPoint, OcrPoint, OcrPoint]:
    points = _sequence(value, "rec_polygon")
    if len(points) != 4:
        raise OcrBackendError("PaddleOCR polygon must contain four points")
    converted = []
    for point in points:
        axes = _sequence(point, "rec_point")
        if len(axes) != 2:
            raise OcrBackendError("PaddleOCR point must contain x and y")
        try:
            converted.append(OcrPoint(axes[0], axes[1]))
        except ValueError as exc:
            raise OcrBackendError(str(exc)) from exc
    return converted[0], converted[1], converted[2], converted[3]


def _sequence(value: object, name: str) -> list[object]:
    converter = getattr(value, "tolist", None)
    converted = converter() if callable(converter) else value
    if isinstance(converted, (str, bytes)):
        raise OcrBackendError(f"PaddleOCR {name} is invalid")
    try:
        return list(converted)  # type: ignore[arg-type]
    except TypeError as exc:
        raise OcrBackendError(f"PaddleOCR {name} is invalid") from exc


def _positive_int(name: str, value: object) -> int:
    if isinstance(value, bool):
        raise OcrBackendError(f"PaddleOCR {name} is invalid")
    try:
        converted = int(value)  # type: ignore[arg-type]
    except (TypeError, ValueError, OverflowError) as exc:
        raise OcrBackendError(f"PaddleOCR {name} is invalid") from exc
    if converted < 1 or converted != value:
        raise OcrBackendError(f"PaddleOCR {name} is invalid")
    return converted


def _validate_models(
    raw: Mapping[str, Path],
) -> tuple[dict[str, Path], str, tuple[TreeGuard, ...]]:
    if set(raw) != _MODEL_KEYS:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: local PaddleOCR det and rec models are required"
        )
    paths = {key: value.resolve() for key, value in raw.items()}
    hashes: dict[str, dict[str, str]] = {}
    guards = []
    for key, directory in sorted(paths.items()):
        try:
            guard = snapshot_tree(directory)
        except ArtifactSafetyError as exc:
            raise OcrNotConfigured(
                f"NOT_CONFIGURED: local PaddleOCR {key} model is unsafe"
            ) from exc
        if not guard.files:
            raise OcrNotConfigured(
                f"NOT_CONFIGURED: local PaddleOCR {key} model is invalid"
            )
        hashes[key] = guard.hashes
        guards.append(guard)
    return paths, _canonical_digest(hashes), tuple(guards)


def _canonical_digest(value: object) -> str:
    data = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(data).hexdigest()
