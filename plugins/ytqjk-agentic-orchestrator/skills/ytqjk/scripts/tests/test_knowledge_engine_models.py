from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
DASHBOARD = SCRIPTS.parent / "dashboard"
sys.path.insert(0, str(SCRIPTS))
sys.path.insert(0, str(DASHBOARD))

from knowledge_engine_models import (  # noqa: E402
    ModelManifestError,
    picture_classifier_paths,
    read_model_settings,
    verify_model_settings,
)


def _write(root: Path, relative: str, content: bytes) -> str:
    path = root / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(content)
    return hashlib.sha256(content).hexdigest()


def _manifest(root: Path) -> dict[str, str]:
    files = {}
    rapid = {
        "det": "RapidOcr/det.onnx",
        "cls": "RapidOcr/cls.onnx",
        "rec": "RapidOcr/rec.onnx",
        "rec_keys": "RapidOcr/keys.txt",
    }
    for relative in rapid.values():
        files[relative] = _write(root, relative, relative.encode())
    picture = "DocumentFigureClassifier-v2.5"
    for name in ("model.onnx", "config.json", "preprocessor_config.json"):
        relative = f"{picture}/{name}"
        files[relative] = _write(root, relative, name.encode())
    paddle = {
        "text_detection_model_dir": "PaddleOCR/det",
        "text_recognition_model_dir": "PaddleOCR/rec",
    }
    for directory in paddle.values():
        for name in ("inference.pdiparams", "inference.yml"):
            relative = f"{directory}/{name}"
            files[relative] = _write(root, relative, name.encode())
    smol = "SmolVLM/model"
    for name in ("config.json", "model.safetensors"):
        relative = f"{smol}/{name}"
        files[relative] = _write(root, relative, name.encode())
    (root / "manifest.json").write_text(
        json.dumps({
            "schema_version": 1,
            "files": files,
            "rapidocr": rapid,
            "paddleocr": paddle,
            "smolvlm": {"model_dir": smol},
        }),
        encoding="utf-8",
    )
    return files


def test_manifest_selects_one_complete_picture_classifier(
    tmp_path: Path,
) -> None:
    root = tmp_path / "models" / "document-intake"
    root.mkdir(parents=True)
    expected = _manifest(root)
    settings = read_model_settings(tmp_path)
    assert settings is not None
    assert settings.files == expected
    assert set(settings.rapidocr) == {"det", "cls", "rec", "rec_keys"}
    assert set(settings.paddleocr) == {
        "text_detection_model_dir",
        "text_recognition_model_dir",
    }
    assert settings.smolvlm.name == "model"
    selected = picture_classifier_paths(settings.root, settings.files)
    assert selected["model"].name == "model.onnx"
    assert selected["config"].name == "config.json"
    assert selected["preprocessor"].name == "preprocessor_config.json"
    assert len({path.parent for path in selected.values()}) == 1


def test_missing_or_ambiguous_named_classifier_fails_closed(
    tmp_path: Path,
) -> None:
    root = tmp_path / "models" / "document-intake"
    root.mkdir(parents=True)
    files = _manifest(root)
    files.pop("DocumentFigureClassifier-v2.5/model.onnx")
    with pytest.raises(ModelManifestError, match="picture classifier"):
        picture_classifier_paths(root, files)
    files = _manifest(root)
    for name in ("model.onnx", "config.json", "preprocessor_config.json"):
        relative = f"duplicate/{name}"
        files[relative] = _write(root, relative, b"duplicate" + name.encode())
    selected = picture_classifier_paths(root, files)
    assert selected["model"].parent.name == "DocumentFigureClassifier-v2.5"
    for name in ("model.onnx", "config.json", "preprocessor_config.json"):
        relative = (
            "docling-project--DocumentFigureClassifier-v2.5/"
            f"{name}"
        )
        files[relative] = _write(root, relative, b"runtime" + name.encode())
    with pytest.raises(ModelManifestError, match="picture classifier"):
        picture_classifier_paths(root, files)


def test_unlisted_or_changed_manifest_file_is_rejected(
    tmp_path: Path,
) -> None:
    root = tmp_path / "models" / "document-intake"
    root.mkdir(parents=True)
    _manifest(root)
    path = root / "RapidOcr" / "det.onnx"
    path.write_bytes(b"changed")
    with pytest.raises(ModelManifestError, match="manifest"):
        read_model_settings(tmp_path)


def test_cached_settings_reject_later_model_change(tmp_path: Path) -> None:
    root = tmp_path / "models" / "document-intake"
    root.mkdir(parents=True)
    _manifest(root)
    settings = read_model_settings(tmp_path)
    assert settings is not None
    verify_model_settings(settings)
    (root / "RapidOcr" / "det.onnx").write_bytes(b"later-change")
    with pytest.raises(ModelManifestError, match="changed"):
        verify_model_settings(settings)


def test_optional_ppstructure_inventory_is_all_or_nothing(
    tmp_path: Path,
) -> None:
    root = tmp_path / "models" / "document-intake"
    root.mkdir(parents=True)
    _manifest(root)
    manifest = root / "manifest.json"
    value = json.loads(manifest.read_text(encoding="utf-8"))
    roles = (
        "layout_detection_model_dir",
        "text_detection_model_dir",
        "text_recognition_model_dir",
        "table_classification_model_dir",
        "wired_table_structure_recognition_model_dir",
        "wireless_table_structure_recognition_model_dir",
        "wired_table_cells_detection_model_dir",
        "wireless_table_cells_detection_model_dir",
    )
    structure = {}
    for ordinal, role in enumerate(roles):
        directory = f"PPStructure/model-{ordinal}"
        structure[role] = directory
        for name in ("inference.pdiparams", "inference.yml"):
            relative = f"{directory}/{name}"
            value["files"][relative] = _write(
                root, relative, relative.encode()
            )
    value["ppstructure"] = structure
    manifest.write_text(json.dumps(value), encoding="utf-8")
    settings = read_model_settings(tmp_path)
    assert settings is not None
    assert settings.ppstructure is not None
    assert set(settings.ppstructure) == set(roles)
    value["ppstructure"].pop(roles[-1])
    manifest.write_text(json.dumps(value), encoding="utf-8")
    with pytest.raises(ModelManifestError, match="PP-StructureV3"):
        read_model_settings(tmp_path)
