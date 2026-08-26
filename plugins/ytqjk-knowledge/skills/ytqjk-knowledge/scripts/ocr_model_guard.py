"""Stable model guards shared by local OCR adapters."""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path

from scripts.artifact_safety import (
    ArtifactSafetyError,
    FileGuard,
    snapshot_file,
    verify_files,
)


def rapidocr_models(
    raw: Mapping[str, Path],
    required: set[str],
) -> tuple[dict[str, Path], tuple[FileGuard, ...], dict[str, str]]:
    from scripts.image_ocr_backend import OcrNotConfigured
    if set(raw) != required:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: det, cls, rec and rec_keys models are required"
        )
    paths = {}
    guards = []
    hashes = {}
    for name, path in sorted(raw.items()):
        try:
            guard = snapshot_file(path, 8 * 1024 * 1024 * 1024)
        except ArtifactSafetyError as error:
            raise OcrNotConfigured(
                f"NOT_CONFIGURED: local {name} model is unsafe"
            ) from error
        paths[name] = guard.path
        guards.append(guard)
        hashes[name] = guard.sha256
    return paths, tuple(guards), hashes


def verify_rapidocr(
    guards: tuple[FileGuard, ...],
    *,
    loading: bool,
) -> None:
    from scripts.image_ocr_backend import OcrBackendError, OcrNotConfigured
    try:
        verify_files(guards)
    except ArtifactSafetyError as error:
        if loading:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: RapidOCR models changed"
            ) from error
        raise OcrBackendError(
            "RapidOCR models changed during inference"
        ) from error
