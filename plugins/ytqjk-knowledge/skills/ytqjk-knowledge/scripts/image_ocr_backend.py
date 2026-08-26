from __future__ import annotations

import hashlib
import importlib.metadata
import json
import math
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, Mapping, Protocol

from scripts.artifact_safety import FileGuard
from scripts.ocr_model_guard import rapidocr_models as _rapidocr_models
from scripts.ocr_model_guard import verify_rapidocr as _verify_rapidocr


EXPECTED_RAPIDOCR_VERSION = "3.9.2"
MODEL_PARAMETER_KEYS = dict(
    det="Det.model_path", cls="Cls.model_path",
    rec="Rec.model_path", rec_keys="Rec.rec_keys_path",
)


class OcrNotConfigured(RuntimeError): """OCR runtime is unavailable."""


class OcrBackendError(RuntimeError): """OCR output is unsafe."""


def _finite_number(name: str, value: object) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{name} must be a finite number")
    converted = float(value)
    if not math.isfinite(converted):
        raise ValueError(f"{name} must be a finite number")
    return converted


def _canonical_digest(value: object) -> str:
    payload = json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


@dataclass(frozen=True)
class OcrPoint:
    x: float
    y: float

    def __post_init__(self) -> None:
        x = _finite_number("x", self.x)
        y = _finite_number("y", self.y)
        if x < 0 or y < 0:
            raise ValueError("OCR coordinates must be non-negative")


@dataclass(frozen=True)
class OcrTextBlock:
    text: str
    quad: tuple[OcrPoint, OcrPoint, OcrPoint, OcrPoint]
    confidence: float

    def __post_init__(self) -> None:
        if not isinstance(self.text, str) or not self.text.strip():
            raise ValueError("OCR block text must be non-empty")
        if not isinstance(self.quad, tuple) or len(self.quad) != 4:
            raise ValueError("OCR block quad must contain four points")
        if any(type(point) is not OcrPoint for point in self.quad):
            raise ValueError("OCR block quad contains an invalid point")
        confidence = _finite_number("confidence", self.confidence)
        if confidence < 0 or confidence > 1:
            raise ValueError("OCR confidence must be between 0 and 1")


@dataclass(frozen=True)
class OcrEngineEvidence:
    engine: str
    package_version: str
    model_digest: str
    config_digest: str

    def __post_init__(self) -> None:
        identities = (self.engine, self.package_version)
        if not all(isinstance(x, str) and x.strip() for x in identities):
            raise ValueError("OCR engine identity must be non-empty")
        for name in ("model_digest", "config_digest"):
            value = getattr(self, name)
            valid = isinstance(value, str) and len(value) == 64
            if valid:
                valid = all(char in "0123456789abcdef" for char in value)
            if not valid:
                raise ValueError(f"{name} must be a lowercase SHA-256 digest")


@dataclass(frozen=True)
class OcrBackendResult:
    width: int
    height: int
    blocks: tuple[OcrTextBlock, ...]
    elapsed_ms: int
    evidence: OcrEngineEvidence

    def __post_init__(self) -> None:
        for name in ("width", "height"):
            value = getattr(self, name)
            valid = isinstance(value, int) and not isinstance(value, bool)
            if not valid or value < 1:
                raise ValueError(f"{name} must be a positive integer")
        if not isinstance(self.blocks, tuple):
            raise ValueError("blocks must be a tuple")
        if any(type(block) is not OcrTextBlock for block in self.blocks):
            raise ValueError("blocks contains an invalid OCR block")
        if (
            isinstance(self.elapsed_ms, bool)
            or not isinstance(self.elapsed_ms, int)
            or self.elapsed_ms < 0
        ):
            raise ValueError("elapsed_ms must be a non-negative integer")
        if type(self.evidence) is not OcrEngineEvidence:
            raise ValueError("OCR engine evidence is invalid")


class OcrBackend(Protocol):
    def recognize(self, image_bytes: bytes) -> OcrBackendResult: ...


class RapidOcrBackend:
    def __init__(
        self,
        model_paths: Mapping[str, str | Path],
        *,
        params: Mapping[str, str | int | float | bool] | None = None,
        engine_factory: Callable[..., object] | None = None,
    ) -> None:
        self._model_paths = {k: Path(v) for k, v in model_paths.items()}
        self._params = dict(params or {})
        for name, value in self._params.items():
            if not isinstance(name, str) or not name.strip():
                raise ValueError("RapidOCR parameter names must be text")
            if not isinstance(value, (str, int, float, bool)):
                raise ValueError("RapidOCR parameter values must be scalar")
            if isinstance(value, float) and not math.isfinite(value):
                raise ValueError("RapidOCR float parameters must be finite")
        self._engine_factory = engine_factory
        self._engine: object | None = None
        self._evidence: OcrEngineEvidence | None = None
        self._guards: tuple[FileGuard, ...] = ()

    def recognize(self, image_bytes: bytes) -> OcrBackendResult:
        if not isinstance(image_bytes, bytes) or not image_bytes:
            raise OcrBackendError("image input must be non-empty bytes")
        engine, evidence = self._load_engine()
        _verify_rapidocr(self._guards, loading=False)
        prepared: object = image_bytes
        shape = None
        try:
            loader = getattr(engine, "load_img", None)
            if callable(loader):
                prepared = loader(image_bytes)
                shape = getattr(prepared, "shape", None)
            output = engine(prepared)
            _verify_rapidocr(self._guards, loading=False)
        except Exception as exc:
            raise OcrBackendError(f"RapidOCR inference failed: {exc}") from exc
        return self._convert_output(output, evidence, shape)

    def _load_engine(
        self,
    ) -> tuple[Callable[[object], object], OcrEngineEvidence]:
        if self._engine is not None and self._evidence is not None:
            _verify_rapidocr(self._guards, loading=True)
            return self._engine, self._evidence  # type: ignore[return-value]
        paths, guards, model_hashes = _rapidocr_models(
            self._model_paths,
            set(MODEL_PARAMETER_KEYS),
        )
        version = self._rapidocr_version()
        factory = self._engine_factory
        if factory is None:
            try:
                from rapidocr import RapidOCR
            except (ImportError, ModuleNotFoundError) as exc:
                raise OcrNotConfigured(
                    "NOT_CONFIGURED: rapidocr runtime is unavailable"
                ) from exc
            factory = RapidOCR
        model_digest = _canonical_digest(model_hashes)
        public_config = {
            "engine": "rapidocr-onnxruntime",
            "package_version": version,
            "model_hashes": model_hashes,
            "params": self._params,
        }
        evidence = OcrEngineEvidence(
            "rapidocr-onnxruntime",
            version,
            model_digest,
            _canonical_digest(public_config),
        )
        runtime_params = dict(self._params)
        for name, parameter in MODEL_PARAMETER_KEYS.items():
            runtime_params[parameter] = str(paths[name])
        try:
            engine = factory(params=runtime_params)
        except (ImportError, ModuleNotFoundError) as exc:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: ONNX runtime is unavailable"
            ) from exc
        except Exception as exc:
            raise OcrNotConfigured(
                f"NOT_CONFIGURED: RapidOCR models cannot load: {exc}"
            ) from exc
        if not callable(engine):
            raise OcrBackendError("RapidOCR engine is not callable")
        _verify_rapidocr(guards, loading=True)
        self._engine = engine
        self._evidence = evidence
        self._guards = guards
        return engine, evidence

    @staticmethod
    def _rapidocr_version() -> str:
        try:
            version = importlib.metadata.version("rapidocr")
        except importlib.metadata.PackageNotFoundError as exc:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: rapidocr 3.9.2 is not installed"
            ) from exc
        if version != EXPECTED_RAPIDOCR_VERSION:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: rapidocr version must be 3.9.2"
            )
        return version

    @staticmethod
    def _convert_output(
        output: object,
        evidence: OcrEngineEvidence,
        fallback_shape: object = None,
    ) -> OcrBackendResult:
        image = getattr(output, "img", None)
        shape = getattr(image, "shape", None)
        if shape is None:
            shape = fallback_shape
        if shape is None or len(shape) < 2:
            raise OcrBackendError("RapidOCR output has no image dimensions")
        height = RapidOcrBackend._dimension("height", shape[0])
        width = RapidOcrBackend._dimension("width", shape[1])
        boxes = getattr(output, "boxes", None)
        texts = getattr(output, "txts", None)
        scores = getattr(output, "scores", None)
        if boxes is None and texts is None and scores is None:
            blocks: tuple[OcrTextBlock, ...] = ()
        else:
            sequences = [RapidOcrBackend._as_list(item) for item in (
                boxes, texts, scores
            )]
            if len({len(item) for item in sequences}) != 1:
                raise OcrBackendError("RapidOCR output lengths do not match")
            blocks = tuple(
                RapidOcrBackend._convert_block(box, text, score)
                for box, text, score in zip(*sequences, strict=True)
            )
        elapsed_value = getattr(output, "elapse", 0)
        if elapsed_value is None and not blocks:
            elapsed_value = 0
        elapsed = _finite_number("elapse", elapsed_value)
        if elapsed < 0:
            raise OcrBackendError("RapidOCR elapsed time must be non-negative")
        return OcrBackendResult(
            width, height, blocks, round(elapsed * 1000), evidence,
        )

    @staticmethod
    def _as_list(value: object) -> list[object]:
        if value is None:
            raise OcrBackendError("RapidOCR output is partially missing")
        converter = getattr(value, "tolist", None)
        converted = converter() if callable(converter) else value
        if isinstance(converted, (str, bytes)):
            raise OcrBackendError("RapidOCR output sequence is invalid")
        try:
            return list(converted)  # type: ignore[arg-type]
        except TypeError as exc:
            message = "RapidOCR output sequence is invalid"
            raise OcrBackendError(message) from exc

    @staticmethod
    def _dimension(name: str, value: object) -> int:
        if isinstance(value, bool):
            raise OcrBackendError(f"RapidOCR {name} is invalid")
        try:
            converted = int(value)  # type: ignore[arg-type]
        except (TypeError, ValueError, OverflowError) as exc:
            raise OcrBackendError(f"RapidOCR {name} is invalid") from exc
        if converted < 1 or converted != value:
            raise OcrBackendError(f"RapidOCR {name} is invalid")
        return converted

    @staticmethod
    def _convert_block(
        box: object,
        text: object,
        score: object,
    ) -> OcrTextBlock:
        points = RapidOcrBackend._as_list(box)
        if len(points) != 4:
            raise OcrBackendError("RapidOCR box must contain four points")
        quad: list[OcrPoint] = []
        for point in points:
            values = RapidOcrBackend._as_list(point)
            if len(values) != 2:
                raise OcrBackendError("RapidOCR point must contain x and y")
            try:
                quad.append(OcrPoint(values[0], values[1]))
            except ValueError as exc:
                raise OcrBackendError(str(exc)) from exc
        if not isinstance(text, str):
            raise OcrBackendError("RapidOCR text must be text")
        try:
            fixed_quad = (quad[0], quad[1], quad[2], quad[3])
            return OcrTextBlock(text, fixed_quad, score)
        except ValueError as exc:
            raise OcrBackendError(str(exc)) from exc
