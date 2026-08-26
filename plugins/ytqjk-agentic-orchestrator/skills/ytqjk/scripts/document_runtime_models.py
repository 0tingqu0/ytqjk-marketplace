"""Model manifest contract for local document-intake runtime."""

from __future__ import annotations

import hashlib
from pathlib import PurePosixPath


SCHEMA_VERSION = 1
DOCLING_LAYOUT = "docling-project--docling-layout-heron-onnx"
DOCLING_TABLE = "docling-project--docling-models"
DOCLING_CLASSIFIER = "docling-project--DocumentFigureClassifier-v2.5"
PADDLE_DET = "PaddleOCR/PP-OCRv6_medium_det"
PADDLE_REC = "PaddleOCR/PP-OCRv6_medium_rec"
PADDLE_LAYOUT = "PPStructure/PP-DocLayout_plus-L"
PADDLE_TABLE_CLASSIFIER = "PPStructure/PP-LCNet_x1_0_table_cls"
PADDLE_WIRED_STRUCTURE = "PPStructure/SLANeXt_wired"
PADDLE_WIRELESS_STRUCTURE = "PPStructure/SLANet_plus"
PADDLE_WIRED_CELLS = "PPStructure/RT-DETR-L_wired_table_cell_det"
PADDLE_WIRELESS_CELLS = (
    "PPStructure/RT-DETR-L_wireless_table_cell_det"
)
SMOLVLM = "HuggingFaceTB--SmolVLM-256M-Instruct"
PPSTRUCTURE = {
    "layout_detection_model_dir": PADDLE_LAYOUT,
    "text_detection_model_dir": PADDLE_DET,
    "text_recognition_model_dir": PADDLE_REC,
    "table_classification_model_dir": PADDLE_TABLE_CLASSIFIER,
    "wired_table_structure_recognition_model_dir": (
        PADDLE_WIRED_STRUCTURE
    ),
    "wireless_table_structure_recognition_model_dir": (
        PADDLE_WIRELESS_STRUCTURE
    ),
    "wired_table_cells_detection_model_dir": PADDLE_WIRED_CELLS,
    "wireless_table_cells_detection_model_dir": PADDLE_WIRELESS_CELLS,
}


class RuntimeModelError(RuntimeError):
    def __init__(self, code: str) -> None:
        super().__init__(code)
        self.code = code


def build_manifest(files: dict[str, str]) -> dict[str, object]:
    _require_directory(
        files,
        DOCLING_LAYOUT,
        {"config.json", "model.onnx", "preprocessor_config.json"},
    )
    _require_paths(files, {
        f"{DOCLING_TABLE}/model_artifacts/tableformer/accurate/"
        "tableformer_accurate.safetensors",
        f"{DOCLING_TABLE}/model_artifacts/tableformer/accurate/"
        "tm_config.json",
        f"{DOCLING_TABLE}/model_artifacts/tableformer/fast/"
        "tableformer_fast.safetensors",
        f"{DOCLING_TABLE}/model_artifacts/tableformer/fast/tm_config.json",
    })
    _require_directory(
        files,
        DOCLING_CLASSIFIER,
        {"config.json", "model.onnx", "preprocessor_config.json"},
    )
    _require_directory(
        files,
        PADDLE_DET,
        {"inference.pdiparams", "inference.yml"},
    )
    _require_directory(
        files,
        PADDLE_REC,
        {"inference.pdiparams", "inference.yml"},
    )
    _require_directory(
        files,
        SMOLVLM,
        {"config.json", "model.safetensors"},
    )
    for directory in set(PPSTRUCTURE.values()):
        _require_directory(
            files,
            directory,
            {"inference.pdiparams", "inference.yml"},
        )
    return {
        "schema_version": SCHEMA_VERSION,
        "files": files,
        "rapidocr": _rapidocr_roles(files),
        "paddleocr": {
            "text_detection_model_dir": PADDLE_DET,
            "text_recognition_model_dir": PADDLE_REC,
        },
        "smolvlm": {"model_dir": SMOLVLM},
        "ppstructure": dict(PPSTRUCTURE),
    }


def validate_manifest(
    value: dict[str, object],
    files: dict[str, str],
) -> dict[str, object]:
    expected = build_manifest(files)
    if value.get("schema_version") != SCHEMA_VERSION:
        raise RuntimeModelError("MODEL_MANIFEST_INVALID")
    if value.get("files") != files:
        raise RuntimeModelError("MODEL_DIGEST_MISMATCH")
    for name, code in (
        ("rapidocr", "RAPIDOCR_MANIFEST_INVALID"),
        ("paddleocr", "PADDLEOCR_MANIFEST_INVALID"),
        ("smolvlm", "SMOLVLM_MANIFEST_INVALID"),
        ("ppstructure", "PPSTRUCTURE_MANIFEST_INVALID"),
    ):
        if value.get(name) != expected[name]:
            raise RuntimeModelError(code)
    if set(value) != set(expected):
        raise RuntimeModelError("MODEL_MANIFEST_INVALID")
    return {
        "file_count": len(files),
        "tree_sha256": tree_digest(files),
        "rapidocr": expected["rapidocr"],
        "paddleocr": expected["paddleocr"],
        "smolvlm": expected["smolvlm"],
        "ppstructure": expected["ppstructure"],
    }


def _rapidocr_roles(files: dict[str, str]) -> dict[str, str]:
    found = {role: [] for role in ("det", "cls", "rec", "rec_keys")}
    for relative in files:
        path = PurePosixPath(relative)
        parts = tuple(part.casefold() for part in path.parts)
        if "rapidocr" not in parts:
            continue
        stem = path.stem.casefold()
        if path.suffix.casefold() == ".onnx" and "onnx" in parts:
            for role in ("det", "cls", "rec"):
                valid = role in parts and role in stem
                if valid and stem.startswith("ch_"):
                    found[role].append(relative)
        if path.suffix.casefold() == ".txt" and "rec" in parts:
            if "keys" in stem or "dict" in stem:
                found["rec_keys"].append(relative)
    if any(len(paths) != 1 for paths in found.values()):
        raise RuntimeModelError("MODEL_ROLE_AMBIGUOUS")
    return {role: paths[0] for role, paths in found.items()}


def _require_paths(files: dict[str, str], required: set[str]) -> None:
    if not required <= set(files):
        raise RuntimeModelError("MODEL_FAMILY_MISSING")


def _require_directory(
    files: dict[str, str],
    directory: str,
    required: set[str],
) -> None:
    prefix = f"{directory}/"
    names = {
        PurePosixPath(path).name
        for path in files
        if path.startswith(prefix)
    }
    if not required <= names:
        raise RuntimeModelError("MODEL_FAMILY_MISSING")


def tree_digest(files: dict[str, str]) -> str:
    digest = hashlib.sha256()
    for relative, value in sorted(files.items()):
        digest.update(relative.encode("utf-8") + b"\0")
        digest.update(value.encode("ascii") + b"\n")
    return digest.hexdigest()


__all__ = [
    "DOCLING_CLASSIFIER",
    "DOCLING_LAYOUT",
    "DOCLING_TABLE",
    "PADDLE_DET",
    "PADDLE_LAYOUT",
    "PADDLE_REC",
    "PADDLE_TABLE_CLASSIFIER",
    "PADDLE_WIRED_CELLS",
    "PADDLE_WIRED_STRUCTURE",
    "PADDLE_WIRELESS_CELLS",
    "PADDLE_WIRELESS_STRUCTURE",
    "PPSTRUCTURE",
    "RuntimeModelError",
    "SMOLVLM",
    "build_manifest",
    "tree_digest",
    "validate_manifest",
]
