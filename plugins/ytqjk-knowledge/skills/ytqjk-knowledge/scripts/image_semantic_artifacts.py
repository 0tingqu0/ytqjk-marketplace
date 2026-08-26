"""Fail-closed local artifact loading for image semantics."""

from __future__ import annotations

import hashlib
import json
import math
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path

from scripts.artifact_safety import (
    ArtifactSafetyError,
    FileGuard,
    read_bytes,
    snapshot_file,
    verify_files,
)

from scripts.image_ocr_backend import OcrNotConfigured
from scripts.image_semantic_contract import EXPECTED_LABELS


_ARTIFACT_KEYS = frozenset(("model", "config", "preprocessor"))


@dataclass(frozen=True)
class SemanticArtifacts:
    model: Path
    labels: tuple[str, ...]
    mean: tuple[float, ...]
    std: tuple[float, ...]
    rescale_factor: float
    size: tuple[int, int]
    config_digest: str
    guards: tuple[FileGuard, ...]


def _canonical_digest(value: object) -> str:
    payload = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def _paths(
    artifacts: Mapping[str, str | Path],
) -> tuple[dict[str, Path], dict[str, FileGuard]]:
    if set(artifacts) != _ARTIFACT_KEYS:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: complete picture classifier is required"
        )
    resolved = {}
    guards = {}
    parent: Path | None = None
    for key, raw_path in artifacts.items():
        path = Path(raw_path)
        try:
            guard = snapshot_file(path, 8 * 1024 * 1024 * 1024)
        except ArtifactSafetyError as error:
            raise OcrNotConfigured(
                f"NOT_CONFIGURED: picture classifier {key} is unsafe"
            ) from error
        value = guard.path
        if parent is None:
            parent = value.parent
        if value.parent != parent:
            raise OcrNotConfigured(
                "NOT_CONFIGURED: classifier artifacts are separated"
            )
        resolved[key] = value
        guards[key] = guard
    return resolved, guards


def _json(path: Path) -> dict[str, object]:
    try:
        _, content = read_bytes(path, 16 * 1024 * 1024)
        value = json.loads(content.decode("utf-8"))
    except (
        OSError,
        UnicodeError,
        json.JSONDecodeError,
        ArtifactSafetyError,
    ) as error:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier metadata is invalid"
        ) from error
    if not isinstance(value, dict):
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier metadata is invalid"
        )
    return value


def _labels(config: dict[str, object]) -> tuple[str, ...]:
    raw = config.get("id2label")
    if not isinstance(raw, dict):
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier label contract is invalid"
        )
    try:
        labels = tuple(raw[str(index)] for index in range(len(raw)))
    except KeyError as error:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier label contract is invalid"
        ) from error
    if labels != EXPECTED_LABELS:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier label contract is invalid"
        )
    return labels


def _finite_sequence(value: object, name: str) -> tuple[float, ...]:
    if not isinstance(value, list) or len(value) != 3:
        raise OcrNotConfigured(f"NOT_CONFIGURED: invalid {name}")
    if any(isinstance(item, bool) for item in value):
        raise OcrNotConfigured(f"NOT_CONFIGURED: invalid {name}")
    try:
        result = tuple(float(item) for item in value)
    except (TypeError, ValueError) as error:
        raise OcrNotConfigured(
            f"NOT_CONFIGURED: invalid {name}"
        ) from error
    if any(not math.isfinite(item) for item in result):
        raise OcrNotConfigured(f"NOT_CONFIGURED: invalid {name}")
    return result


def _processor(
    value: dict[str, object],
) -> tuple[
    tuple[float, ...],
    tuple[float, ...],
    float,
    tuple[int, int],
]:
    required = ("do_normalize", "do_rescale", "do_resize")
    if any(value.get(name) is not True for name in required):
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier preprocessing is invalid"
        )
    mean = _finite_sequence(value.get("image_mean"), "mean")
    std = _finite_sequence(value.get("image_std"), "std")
    factor = value.get("rescale_factor")
    if any(item <= 0 for item in std):
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier preprocessing is invalid"
        )
    if isinstance(factor, bool) or not isinstance(factor, (int, float)):
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier rescale factor is invalid"
        )
    factor = float(factor)
    if not math.isfinite(factor) or factor <= 0:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier rescale factor is invalid"
        )
    raw_size = value.get("size")
    if not isinstance(raw_size, dict):
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier image size is invalid"
        )
    width = raw_size.get("width")
    height = raw_size.get("height")
    valid = all(
        isinstance(item, int)
        and not isinstance(item, bool)
        and 1 <= item <= 4096
        for item in (width, height)
    )
    if not valid:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier image size is invalid"
        )
    return mean, std, factor, (width, height)


def load_artifacts(
    artifacts: Mapping[str, str | Path],
) -> SemanticArtifacts:
    paths, guards = _paths(artifacts)
    labels = _labels(_json(paths["config"]))
    mean, std, factor, size = _processor(
        _json(paths["preprocessor"])
    )
    hashes = {
        key: guards[key].sha256 for key in sorted(guards)
    }
    config_digest = _canonical_digest({
        "artifacts": hashes,
        "labels": labels,
        "mean": mean,
        "rescale_factor": factor,
        "size": size,
        "std": std,
    })
    ordered = tuple(guards[key] for key in sorted(guards))
    try:
        verify_files(ordered)
    except ArtifactSafetyError as error:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: classifier artifacts changed"
        ) from error
    return SemanticArtifacts(
        paths["model"],
        labels,
        mean,
        std,
        factor,
        size,
        config_digest,
        ordered,
    )


__all__ = ["SemanticArtifacts", "load_artifacts"]
