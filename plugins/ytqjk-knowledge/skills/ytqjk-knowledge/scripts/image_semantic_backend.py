"""Offline ONNX image semantics for searchable knowledge candidates."""

from __future__ import annotations

import importlib.metadata
import math
import time
from collections.abc import Mapping
from dataclasses import replace
from io import BytesIO
from pathlib import Path
from typing import Callable

import numpy as np
from PIL import Image, ImageOps, UnidentifiedImageError

from scripts.artifact_safety import ArtifactSafetyError, verify_files

from scripts.image_document_extractor import ImageFeatures
from scripts.image_ocr_backend import OcrNotConfigured
from scripts.image_semantic_artifacts import (
    SemanticArtifacts,
    load_artifacts,
)
from scripts.image_semantic_contract import (
    ENGINE_NAME,
    MODEL_NAME,
    build_classification,
)
from scripts.intake_extraction_contracts import (
    ImageClassification,
    RecognitionEvidence,
)


MAX_IMAGE_PIXELS = 50_000_000
MAX_IMAGE_BYTES = 50 * 1024 * 1024


class OnnxImageSemanticClassifier:
    """Classify image pixels with Docling's local figure model."""

    def __init__(
        self,
        artifacts: Mapping[str, str | Path],
        *,
        session_factory: Callable[..., object] | None = None,
        runtime_version: str | None = None,
    ) -> None:
        self._artifact_paths = dict(artifacts)
        self._factory = session_factory
        self._runtime_version = runtime_version
        self._session: object | None = None
        self._artifacts: SemanticArtifacts | None = None
        self._evidence: RecognitionEvidence | None = None

    def classify(
        self,
        image_bytes: bytes,
        features: ImageFeatures,
    ) -> ImageClassification:
        del features
        session, artifacts, evidence = self._load()
        self._verify(artifacts, False)
        tensor = self._prepare(image_bytes, artifacts)
        started = time.perf_counter()
        try:
            inputs = session.get_inputs()
            if len(inputs) != 1 or not isinstance(inputs[0].name, str):
                raise ValueError("classifier input contract is invalid")
            output = session.run(None, {inputs[0].name: tensor})
            self._verify(artifacts, False)
        except ValueError:
            raise
        except Exception as error:
            raise ValueError("classifier inference failed") from error
        elapsed = round((time.perf_counter() - started) * 1000)
        scores = self._probabilities(output, len(artifacts.labels))
        order = np.argsort(scores)[::-1]
        return replace(
            build_classification(
                artifacts.labels,
                scores,
                order,
                evidence,
            ),
            elapsed_ms=elapsed,
        )

    def _load(
        self,
    ) -> tuple[object, SemanticArtifacts, RecognitionEvidence]:
        if (
            self._session is not None
            and self._artifacts is not None
            and self._evidence is not None
        ):
            self._verify(self._artifacts, True)
            return self._session, self._artifacts, self._evidence
        artifacts = load_artifacts(self._artifact_paths)
        version = self._version()
        evidence = RecognitionEvidence(
            ENGINE_NAME,
            f"{MODEL_NAME}:onnxruntime:{version}",
            artifacts.config_digest,
        )
        session = self._new_session(artifacts.model)
        self._verify(artifacts, True)
        self._session = session
        self._artifacts = artifacts
        self._evidence = evidence
        return session, artifacts, evidence

    @staticmethod
    def _verify(artifacts: SemanticArtifacts, loading: bool) -> None:
        try:
            verify_files(artifacts.guards)
        except ArtifactSafetyError as error:
            if loading:
                raise OcrNotConfigured(
                    "NOT_CONFIGURED: classifier artifacts changed"
                ) from error
            raise ValueError(
                "classifier artifacts changed during inference"
            ) from error

    def _version(self) -> str:
        if self._runtime_version is not None:
            return self._runtime_version
        try:
            return importlib.metadata.version("onnxruntime")
        except importlib.metadata.PackageNotFoundError as error:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: onnxruntime is unavailable"
            ) from error

    def _new_session(self, model: Path) -> object:
        factory = self._factory
        if factory is None:
            try:
                from onnxruntime import InferenceSession
            except (ImportError, ModuleNotFoundError) as error:
                raise OcrNotConfigured(
                    "NOT_CONFIGURED: onnxruntime is unavailable"
                ) from error
            factory = InferenceSession
        try:
            return factory(
                str(model),
                providers=["CPUExecutionProvider"],
            )
        except Exception as error:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: picture classifier cannot load"
            ) from error

    @staticmethod
    def _prepare(
        image_bytes: bytes,
        artifacts: SemanticArtifacts,
    ) -> np.ndarray:
        if not isinstance(image_bytes, bytes) or not image_bytes:
            raise ValueError("image input must be non-empty bytes")
        if len(image_bytes) > MAX_IMAGE_BYTES:
            raise ValueError("image byte limit exceeded")
        try:
            with Image.open(BytesIO(image_bytes)) as opened:
                if opened.width * opened.height > MAX_IMAGE_PIXELS:
                    raise ValueError("image pixel limit exceeded")
                image = ImageOps.exif_transpose(opened)
                if image.width * image.height > MAX_IMAGE_PIXELS:
                    raise ValueError("image pixel limit exceeded")
                image = image.convert("RGB")
                resized = image.resize(
                    artifacts.size,
                    resample=Image.Resampling.BILINEAR,
                )
                array = np.asarray(resized, dtype=np.float32)
        except (UnidentifiedImageError, OSError) as error:
            raise ValueError("image decoding failed") from error
        array = array * artifacts.rescale_factor
        mean = np.asarray(artifacts.mean, dtype=np.float32)
        std = np.asarray(artifacts.std, dtype=np.float32)
        array = (array - mean) / std
        return np.transpose(array, (2, 0, 1))[None, ...]

    @staticmethod
    def _probabilities(output: object, labels: int) -> np.ndarray:
        if not isinstance(output, (list, tuple)) or len(output) != 1:
            raise ValueError("classifier output contract is invalid")
        raw = np.asarray(output[0])
        if raw.dtype == np.bool_:
            raise ValueError("classifier output contract is invalid")
        logits = np.asarray(raw, dtype=np.float64)
        if logits.shape != (1, labels) or not np.isfinite(logits).all():
            raise ValueError("classifier output contract is invalid")
        shifted = logits[0] - np.max(logits[0])
        values = np.exp(shifted)
        total = float(values.sum())
        if not math.isfinite(total) or total <= 0:
            raise ValueError("classifier output contract is invalid")
        return values / total


__all__ = [
    "ENGINE_NAME",
    "MODEL_NAME",
    "OnnxImageSemanticClassifier",
]
