"""Lazy, offline-only Docling backend for PDF extraction."""

from __future__ import annotations

import importlib
import importlib.metadata
import re
import time
from collections.abc import Mapping
from io import BytesIO
from pathlib import Path
from typing import Any

from scripts.artifact_safety import (
    ArtifactSafetyError,
    TreeGuard,
    verify_tree,
)
from scripts.docling_artifacts import is_link as _is_link
from scripts.docling_artifacts import verified_artifacts

from scripts.docling_error_mapping import map_docling_error
from scripts.docling_layout_runtime import configure_layout
from scripts.docling_picture_runtime import (
    configure_picture,
    docling_config_digest,
    picture_evidence,
)
from scripts.docling_payload_parser import parse_docling_payload
from scripts.intake_extraction_contracts import RecognitionEvidence
from scripts.pdf_document_extractor import (
    BackendDocument,
    BackendPage,
    PdfExtractionError,
    PdfLimits,
)


DOCLING_VERSION = "2.121.0"
RAPIDOCR_VERSION = "3.9.2"
_DIGEST = re.compile(r"^[0-9a-f]{64}$")
_RAPID_KEYS = frozenset(("det", "cls", "rec", "keys"))


def _not_configured(message: str) -> PdfExtractionError:
    return PdfExtractionError("NOT_CONFIGURED", message)


def _safe_mapping(value: object) -> dict[str, str]:
    return dict(value) if isinstance(value, Mapping) else {}


def _relative_path(value: str) -> str:
    path = Path(value)
    if not value or path.is_absolute() or path.drive:
        raise _not_configured("model manifest path must be relative")
    if any(part in ("", ".", "..") for part in path.parts):
        raise _not_configured("model manifest path escapes its root")
    return path.as_posix()


class DoclingBackend:
    _mapped_error = staticmethod(map_docling_error)
    def __init__(
        self,
        artifacts_path: Path | None = None,
        artifact_hashes: Mapping[str, str] | None = None,
        rapidocr_paths: Mapping[str, str] | None = None,
        picture_model_path: Path | None = None,
        timeout_seconds: int = 90,
    ) -> None:
        self._artifacts = artifacts_path
        self._hashes = _safe_mapping(artifact_hashes)
        self._rapid = _safe_mapping(rapidocr_paths)
        self._picture_model = picture_model_path
        self._timeout = timeout_seconds
        self._verified: tuple[Path, str, dict[str, Path]] | None = None
        self._artifact_guard: TreeGuard | None = None
        self._converters: dict[tuple[bool, bool], object] = {}

    def extract_native(self, data: bytes, limits: PdfLimits) -> BackendDocument:
        return self._convert(data, limits, False, None)

    def extract_ocr(
        self,
        source: bytes,
        page_numbers: tuple[int, ...],
        limits: PdfLimits,
    ) -> BackendDocument:
        pages: list[BackendPage] = []
        elapsed = 0
        for number in page_numbers:
            result = self._convert(source, limits, True, (number, number))
            if len(result.pages) != 1 or result.pages[0].number != number:
                raise PdfExtractionError(
                    "BACKEND_FAILURE", "Docling returned wrong OCR page"
                )
            pages.append(result.pages[0])
            elapsed += result.elapsed_ms
        return BackendDocument(
            tuple(pages),
            self._evidence(True, False),
            elapsed,
        )

    def _manifest(self) -> tuple[dict[str, str], dict[str, str]]:
        entries = (*self._hashes.items(), *self._rapid.items())
        if not all(isinstance(item, str) for pair in entries for item in pair):
            raise _not_configured("model manifest values must be strings")
        expected: dict[str, str] = {}
        for raw_path, digest in self._hashes.items():
            relative = _relative_path(raw_path)
            if relative in expected or not _DIGEST.fullmatch(digest):
                raise _not_configured("model manifest is invalid")
            expected[relative] = digest
        rapid = {
            key: _relative_path(value) for key, value in self._rapid.items()
        }
        if not expected or rapid.keys() != _RAPID_KEYS:
            raise _not_configured("complete model manifest is required")
        if not set(rapid.values()).issubset(expected):
            raise _not_configured("RapidOCR models are outside the manifest")
        return expected, rapid

    def _verify_artifacts(
        self,
    ) -> tuple[Path, str, dict[str, Path]]:
        if self._verified is not None:
            if self._artifact_guard is None:
                raise _not_configured("Docling artifact guard is missing")
            try:
                verify_tree(self._artifact_guard)
            except ArtifactSafetyError as error:
                raise _not_configured(
                    "Docling model artifacts changed"
                ) from error
            return self._verified
        if self._artifacts is None or self._timeout < 1:
            raise _not_configured(
                "Docling model artifacts are not configured"
            )
        expected, rapid = self._manifest()
        try:
            root, digest, rapid_files, guard = verified_artifacts(
                self._artifacts,
                expected,
                rapid,
            )
        except ArtifactSafetyError as error:
            if str(error) == "DOCLING_ARTIFACT_UNLISTED":
                raise _not_configured("unlisted model artifact is blocked")
            if str(error) == "DOCLING_ARTIFACT_DIGEST":
                raise _not_configured("model artifact digest mismatch")
            raise _not_configured("Docling model artifacts are unsafe")
        if any(_is_link(item.path) for _, item in guard.files):
            raise _not_configured("linked model artifacts are blocked")
        self._artifact_guard = guard
        self._verified = (root, digest, rapid_files)
        return self._verified

    def _evidence(
        self,
        ocr: bool,
        picture: bool,
    ) -> RecognitionEvidence:
        _, tree_digest, _ = self._verify_artifacts()
        model = (
            f"rapidocr/{RAPIDOCR_VERSION}@sha256:{tree_digest}"
            if ocr
            else f"docling-models@sha256:{tree_digest}"
        )
        return RecognitionEvidence(
            f"docling/{DOCLING_VERSION}",
            model,
            docling_config_digest(
                docling_version=DOCLING_VERSION,
                rapidocr_version=RAPIDOCR_VERSION,
                ocr=ocr,
                picture=picture,
                timeout=self._timeout,
                tree_digest=tree_digest,
                rapid_paths=self._rapid,
            ),
        )

    def _runtime(
        self,
        ocr: bool,
    ) -> tuple[object, type[Any], bool]:
        root, _, rapid = self._verify_artifacts()
        guard = self._artifact_guard
        if guard is None:
            raise _not_configured("Docling artifact guard is missing")
        try:
            docling = importlib.metadata.version("docling")
            rapidocr = importlib.metadata.version("rapidocr")
            converter = importlib.import_module("docling.document_converter")
            models = importlib.import_module("docling.datamodel.base_models")
            options = importlib.import_module(
                "docling.datamodel.pipeline_options"
            )
            engines = importlib.import_module(
                "docling.datamodel.object_detection_engine_options"
            )
        except (ImportError, importlib.metadata.PackageNotFoundError) as error:
            raise _not_configured(
                "fixed PDF runtimes are not installed"
            ) from error
        if docling != DOCLING_VERSION or rapidocr != RAPIDOCR_VERSION:
            raise _not_configured("fixed PDF runtime versions are required")
        picture = False
        key = ocr, picture
        if key not in self._converters:
            keywords: dict[str, object] = {
                "artifacts_path": str(root),
                "do_ocr": ocr,
                "do_table_structure": True,
                "enable_remote_services": False,
                "allow_external_plugins": False,
                "document_timeout": self._timeout,
            }
            if ocr:
                keywords["ocr_options"] = options.RapidOcrOptions(
                    backend="onnxruntime",
                    lang=["chinese"],
                    text_score=0.5,
                    use_det=True,
                    use_cls=True,
                    use_rec=True,
                    print_verbose=False,
                    force_full_page_ocr=True,
                    det_model_path=str(rapid["det"]),
                    cls_model_path=str(rapid["cls"]),
                    rec_model_path=str(rapid["rec"]),
                    rec_keys_path=str(rapid["keys"]),
                )
            configure_picture(options, keywords, picture)
            try:
                configure_layout(options, engines, keywords)
                config = options.PdfPipelineOptions(**keywords)
                option = converter.PdfFormatOption(pipeline_options=config)
                self._converters[key] = converter.DocumentConverter(
                    allowed_formats=[models.InputFormat.PDF],
                    format_options={models.InputFormat.PDF: option},
                )
            except (AttributeError, ImportError, TypeError) as error:
                raise _not_configured(
                    "Docling runtime API is unavailable"
                ) from error
            try:
                verify_tree(guard)
            except ArtifactSafetyError as error:
                raise _not_configured(
                    "Docling artifacts changed while loading"
                ) from error
        return self._converters[key], models.DocumentStream, picture

    def _convert(
        self,
        source: bytes,
        limits: PdfLimits,
        ocr: bool,
        page_range: tuple[int, int] | None,
    ) -> BackendDocument:
        converter, stream_type, picture = self._runtime(ocr)
        self._verify_artifacts()
        guard = self._artifact_guard
        if guard is None:
            raise _not_configured("Docling artifact guard is missing")
        stream = stream_type(name="intake.pdf", stream=BytesIO(source))
        arguments: dict[str, object] = {
            "max_num_pages": limits.max_pages,
            "max_file_size": limits.max_bytes,
        }
        if page_range:
            arguments["page_range"] = page_range
        started = time.monotonic()
        try:
            result = converter.convert(stream, **arguments)
            try:
                verify_tree(guard)
            except ArtifactSafetyError as error:
                raise PdfExtractionError(
                    "BACKEND_FAILURE",
                    "Docling artifacts changed during inference",
                ) from error
            status = str(getattr(result.status, "value", result.status))
            if status.lower() != "success":
                raise PdfExtractionError(
                    "PDF_CORRUPT", f"Docling conversion status: {status}"
                )
            payload = result.document.export_to_dict()
        except PdfExtractionError:
            raise
        except FileNotFoundError as error:
            raise _not_configured(
                "Docling model artifacts are incomplete"
            ) from error
        except Exception as error:
            raise self._mapped_error(error) from error
        elapsed = round((time.monotonic() - started) * 1000)
        forced_page = page_range[0] if page_range else None
        evidence = self._evidence(ocr, picture)
        _, tree_digest, _ = self._verify_artifacts()
        description_evidence = (
            picture_evidence(tree_digest, evidence.config_digest)
            if picture
            else None
        )
        pages = parse_docling_payload(
            payload,
            ocr=ocr,
            forced_page=forced_page,
            picture_evidence=description_evidence,
        )
        return BackendDocument(pages, evidence, elapsed)
