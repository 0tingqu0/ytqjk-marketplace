"""Cached, integrity-checked structured document planning."""

from __future__ import annotations

import threading
from pathlib import Path
from types import ModuleType
from typing import Protocol

from knowledge_engine_models import (
    ModelManifestError,
    ModelSettings,
    read_model_settings,
    verify_model_settings,
)
from knowledge_engine_vision import (
    VisionNotConfigured,
    build_pdf_secondary,
    build_vision,
)


_UNSET = object()


class EnginePort(Protocol):
    modules: dict[str, ModuleType]

    def module(self, name: str) -> ModuleType: ...


class EngineNotConfigured(RuntimeError):
    pass


class EngineProcessingError(RuntimeError):
    def __init__(self, category: str, code: str) -> None:
        super().__init__(code)
        self.category = category
        self.code = code


class _FixedExtractor:
    def __init__(self, result: object) -> None:
        self._result = result

    def extract(self, _source: bytes, _name: str) -> object:
        return self._result


class EnginePlanner:
    def __init__(self, root: Path, engine: EnginePort) -> None:
        self._root = root
        self._engine = engine
        self._model_settings: object = _UNSET
        self._image_extractor: object | None = None
        self._pdf_extractor: object | None = None
        self._lock = threading.RLock()

    def __call__(
        self,
        source: bytes,
        name: str,
        purpose: str,
        media: str,
    ) -> object:
        with self._lock:
            result = (
                self._image(source, name)
                if media == "image" else self._pdf(source)
            )
            self._settings()
            structured = self._engine.module("structured_document_intake")
            intake = structured.StructuredDocumentIntake(
                _FixedExtractor(result),
                self._secret_gate(structured),
                self._review_gate(structured),
            )
            return intake.plan(
                structured.SourceInput(name, source, purpose)
            )

    def _image(self, source: bytes, name: str) -> object:
        extractor_module = self._engine.module(
            "image_document_extractor"
        )
        if self._image_extractor is None:
            settings = self._settings()
            try:
                backend, classifier, secondary = build_vision(
                    self._engine.modules,
                    settings,
                )
            except VisionNotConfigured as error:
                raise EngineNotConfigured(str(error)) from error
            self._image_extractor = extractor_module.ImageDocumentExtractor(
                backend,
                classifier=classifier,
                secondary_backend=secondary,
            )
        outcome = self._image_extractor.extract(source, name)
        if outcome.status.value == "NOT_CONFIGURED":
            raise EngineNotConfigured("NOT_CONFIGURED")
        if outcome.status.value != "SUCCEEDED" or outcome.result is None:
            raise EngineProcessingError(
                "OCR_FAILED",
                "OCR_EXTRACTION_FAILED",
            )
        return outcome.result

    def _pdf(self, source: bytes) -> object:
        extractor_module = self._engine.module("pdf_document_extractor")
        if self._pdf_extractor is None:
            settings = self._settings()
            backend_module = self._engine.module("docling_backend")
            if settings is None:
                backend = backend_module.DoclingBackend()
            else:
                backend = backend_module.DoclingBackend(
                    settings.root,
                    settings.files,
                    self._docling_rapid(settings.rapidocr),
                    settings.smolvlm,
                )
            try:
                secondary = build_pdf_secondary(
                    self._engine.modules,
                    settings,
                )
                self._pdf_extractor = (
                    extractor_module.PdfDocumentExtractor(
                        backend,
                        secondary_backend=secondary,
                    )
                )
            except VisionNotConfigured as error:
                raise EngineNotConfigured(str(error)) from error
        try:
            return self._pdf_extractor.extract(source)
        except extractor_module.PdfExtractionError as error:
            if error.code == "NOT_CONFIGURED":
                raise EngineNotConfigured("NOT_CONFIGURED") from error
            raise EngineProcessingError(
                "LAYOUT_FAILED",
                str(error.code),
            ) from error

    @staticmethod
    def _docling_rapid(rapid: dict[str, str]) -> dict[str, str]:
        return {
            "keys" if key == "rec_keys" else key: value
            for key, value in rapid.items()
        }

    def _settings(self) -> ModelSettings | None:
        try:
            if self._model_settings is _UNSET:
                self._model_settings = read_model_settings(self._root)
            settings = self._model_settings
            if settings is None:
                return None
            if not isinstance(settings, ModelSettings):
                raise ModelManifestError("model settings cache is invalid")
            verify_model_settings(settings)
            return settings
        except ModelManifestError as error:
            self._invalidate()
            raise EngineNotConfigured("MODEL_MANIFEST_INVALID") from error

    def _invalidate(self) -> None:
        self._model_settings = _UNSET
        self._image_extractor = None
        self._pdf_extractor = None

    def _secret_gate(self, structured: ModuleType) -> object:
        scanner = self._engine.module("intake_security").LocalScanner()

        class Gate:
            @staticmethod
            def ready() -> bool:
                return scanner.ready()

            @staticmethod
            def assess(content: bytes, phase: str) -> object:
                result = scanner.scan(content, phase)
                state = (
                    structured.GateState.BLOCKED
                    if result.state.value == "BLOCKED"
                    else structured.GateState.CLEAR
                )
                return structured.GateDecision(state)

        return Gate()

    @staticmethod
    def _review_gate(structured: ModuleType) -> object:
        class Gate:
            @staticmethod
            def ready() -> bool:
                return True

            @staticmethod
            def assess(_candidate: object) -> object:
                return structured.GateDecision(
                    structured.GateState.REVIEW_REQUIRED,
                    ("MANUAL_REVIEW_REQUIRED",),
                )

        return Gate()


__all__ = [
    "EngineNotConfigured",
    "EnginePlanner",
    "EngineProcessingError",
]
