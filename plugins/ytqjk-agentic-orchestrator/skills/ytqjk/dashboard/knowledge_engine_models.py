"""Verified document-model manifest access for the dashboard engine."""

from __future__ import annotations

import json
import re
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path

from stable_file import (
    FileSnapshot,
    StableFileError,
    TreeSnapshot,
    read_stable_bytes,
    snapshot_tree,
    verify_file,
    verify_tree,
)


_DIGEST = re.compile(r"^[0-9a-f]{64}$")
_RAPID_KEYS = frozenset(("det", "cls", "rec", "rec_keys"))
_PADDLE_KEYS = frozenset((
    "text_detection_model_dir",
    "text_recognition_model_dir",
))
_PPSTRUCTURE_KEYS = frozenset((
    "layout_detection_model_dir",
    "text_detection_model_dir",
    "text_recognition_model_dir",
    "table_classification_model_dir",
    "wired_table_structure_recognition_model_dir",
    "wireless_table_structure_recognition_model_dir",
    "wired_table_cells_detection_model_dir",
    "wireless_table_cells_detection_model_dir",
))
_PICTURE_FILES = {
    "model.onnx": "model",
    "config.json": "config",
    "preprocessor_config.json": "preprocessor",
}
_PICTURE_ROOTS = frozenset((
    "docling-project--DocumentFigureClassifier-v2.5",
    "DocumentFigureClassifier-v2.5",
))


class ModelManifestError(ValueError):
    """The local model inventory is incomplete or untrusted."""


@dataclass(frozen=True, slots=True)
class ModelSettings:
    root: Path
    files: dict[str, str]
    rapidocr: dict[str, str]
    paddleocr: dict[str, Path]
    smolvlm: Path
    ppstructure: dict[str, Path] | None = None
    manifest_guard: FileSnapshot | None = None
    tree_guard: TreeSnapshot | None = None


def _is_link(path: Path) -> bool:
    try:
        junction = getattr(path, "is_junction", lambda: False)()
        return path.is_symlink() or junction
    except OSError:
        return True


def _relative(value: str) -> str:
    path = Path(value)
    invalid = (
        not value
        or path.is_absolute()
        or bool(path.drive)
        or any(part in ("", ".", "..") for part in path.parts)
    )
    if invalid:
        raise ModelManifestError("model manifest path is invalid")
    return path.as_posix()


def _files(
    value: object,
    actual: Mapping[str, str],
) -> dict[str, str]:
    if not isinstance(value, Mapping) or not value:
        raise ModelManifestError("model manifest files are invalid")
    files = {}
    for raw_relative, expected in value.items():
        if not isinstance(raw_relative, str) or not isinstance(expected, str):
            raise ModelManifestError("model manifest files are invalid")
        relative = _relative(raw_relative)
        if relative in files or _DIGEST.fullmatch(expected) is None:
            raise ModelManifestError("model manifest files are invalid")
        if actual.get(relative) != expected:
            raise ModelManifestError("model manifest digest mismatch")
        files[relative] = expected
    if set(files) != set(actual):
        raise ModelManifestError("model manifest inventory mismatch")
    return files


def read_model_settings(
    knowledge_root: Path,
) -> ModelSettings | None:
    manifest = (
        knowledge_root / "models" / "document-intake" / "manifest.json"
    )
    if not manifest.is_file():
        return None
    try:
        root = manifest.parent.resolve(strict=True)
        root.relative_to(knowledge_root.resolve(strict=True))
        tree = snapshot_tree(
            root,
            100_000,
            32 * 1024 * 1024 * 1024,
            excluded=frozenset(("manifest.json",)),
        )
        manifest_guard, content = read_stable_bytes(
            manifest,
            16 * 1024 * 1024,
        )
        value = json.loads(content.decode("utf-8"))
    except ModelManifestError:
        raise
    except (
        OSError,
        UnicodeError,
        ValueError,
        TypeError,
        json.JSONDecodeError,
        StableFileError,
    ) as error:
        raise ModelManifestError("model manifest is invalid") from error
    if not isinstance(value, dict):
        raise ModelManifestError("model manifest is invalid")
    required = {
        "schema_version",
        "files",
        "rapidocr",
        "paddleocr",
        "smolvlm",
    }
    if set(value) not in (required, required | {"ppstructure"}):
        raise ModelManifestError("model manifest is invalid")
    if value["schema_version"] != 1:
        raise ModelManifestError("model manifest schema is unsupported")
    files = _files(value["files"], tree.hashes)
    rapid = _path_mapping(value["rapidocr"], _RAPID_KEYS, "RapidOCR")
    if not set(rapid.values()) <= set(files):
        raise ModelManifestError("RapidOCR files are outside the manifest")
    paddle = _path_mapping(
        value["paddleocr"],
        _PADDLE_KEYS,
        "PaddleOCR",
    )
    paddle_paths = {
        key: _model_directory(
            root,
            files,
            relative,
            {"inference.pdiparams", "inference.yml"},
        )
        for key, relative in paddle.items()
    }
    smol = _path_mapping(
        value["smolvlm"],
        frozenset(("model_dir",)),
        "SmolVLM",
    )
    smol_path = _model_directory(
        root,
        files,
        smol["model_dir"],
        {"config.json", "model.safetensors"},
    )
    structure_paths = None
    if "ppstructure" in value:
        structure = _path_mapping(
            value["ppstructure"],
            _PPSTRUCTURE_KEYS,
            "PP-StructureV3",
        )
        structure_paths = {
            key: _model_directory(
                root,
                files,
                relative,
                {"inference.pdiparams", "inference.yml"},
            )
            for key, relative in structure.items()
        }
    try:
        verify_tree(tree)
    except StableFileError as error:
        raise ModelManifestError("model artifacts changed") from error
    return ModelSettings(
        root,
        files,
        rapid,
        paddle_paths,
        smol_path,
        structure_paths,
        manifest_guard,
        tree,
    )


def verify_model_settings(settings: ModelSettings) -> None:
    manifest = settings.manifest_guard
    tree = settings.tree_guard
    if manifest is None and tree is None:
        return
    if manifest is None or tree is None:
        raise ModelManifestError("model integrity guards are incomplete")
    try:
        verify_file(manifest)
        verify_tree(tree)
    except StableFileError as error:
        raise ModelManifestError("model artifacts changed") from error


def _path_mapping(
    value: object,
    keys: frozenset[str],
    label: str,
) -> dict[str, str]:
    if not isinstance(value, Mapping):
        raise ModelManifestError(f"{label} manifest is invalid")
    selected = dict(value)
    if set(selected) != keys or not all(
        isinstance(item, str) for item in selected.values()
    ):
        raise ModelManifestError(f"{label} manifest is invalid")
    return {key: _relative(item) for key, item in selected.items()}


def _model_directory(
    root: Path,
    files: Mapping[str, str],
    relative: str,
    required: set[str],
) -> Path:
    try:
        directory = (root / relative).resolve(strict=True)
        directory.relative_to(root.resolve(strict=True))
    except (OSError, ValueError, RuntimeError) as error:
        raise ModelManifestError("model directory is invalid") from error
    if not directory.is_dir() or _is_link(root / relative):
        raise ModelManifestError("model directory is unsafe")
    prefix = f"{Path(relative).as_posix().rstrip('/')}/"
    listed = {
        item for item in files if item.startswith(prefix)
    }
    actual = set()
    for path in directory.rglob("*"):
        if _is_link(path):
            raise ModelManifestError("model directory is unsafe")
        if path.is_file():
            actual.add(path.relative_to(root).as_posix())
    if actual != listed or not required <= {
        Path(item).name for item in actual
    }:
        raise ModelManifestError("model directory is incomplete")
    return directory


def picture_classifier_paths(
    root: Path,
    files: Mapping[str, str],
) -> dict[str, Path]:
    groups: dict[str, dict[str, str]] = {}
    for relative in files:
        path = Path(relative)
        role = _PICTURE_FILES.get(path.name)
        if role is not None:
            parent = path.parent.as_posix()
            groups.setdefault(parent, {})[role] = relative
    required = frozenset(_PICTURE_FILES.values())
    complete = [
        groups[parent]
        for parent in _PICTURE_ROOTS
        if parent in groups and set(groups[parent]) == required
    ]
    if len(complete) != 1:
        raise ModelManifestError(
            "picture classifier manifest must contain one complete model"
        )
    selected = complete[0]
    result = {}
    for role, relative in selected.items():
        try:
            path = (root / relative).resolve(strict=True)
            path.relative_to(root.resolve(strict=True))
        except (OSError, ValueError, RuntimeError) as error:
            raise ModelManifestError(
                "picture classifier path is invalid"
            ) from error
        result[role] = path
    return result


__all__ = [
    "ModelManifestError",
    "ModelSettings",
    "picture_classifier_paths",
    "read_model_settings",
    "verify_model_settings",
]
