from __future__ import annotations

import sys
from pathlib import Path
from types import SimpleNamespace


SCRIPTS = Path(__file__).resolve().parents[1]
DASHBOARD = SCRIPTS.parent / "dashboard"
sys.path.insert(0, str(SCRIPTS))
sys.path.insert(0, str(DASHBOARD))

from knowledge_engine_models import ModelSettings  # noqa: E402
from knowledge_engine_vision import (  # noqa: E402
    build_pdf_secondary,
    build_vision,
)


class Recorder:
    calls: list[tuple[tuple[object, ...], dict[str, object]]] = []

    def __init__(self, *args: object, **kwargs: object) -> None:
        type(self).calls.append((args, kwargs))


def _modules() -> dict[str, object]:
    names = (
        "RapidOcrBackend",
        "OnnxImageSemanticClassifier",
        "SmolVlmDescriber",
        "MergedImageSemanticClassifier",
        "PaddleOcrV3Backend",
        "PaddleStructureV3Backend",
        "PaddlePdfSecondaryBackend",
    )
    classes = {name: type(name, (Recorder,), {"calls": []}) for name in names}
    return {
        "image_ocr_backend": SimpleNamespace(
            RapidOcrBackend=classes["RapidOcrBackend"],
        ),
        "image_semantic_backend": SimpleNamespace(
            OnnxImageSemanticClassifier=(
                classes["OnnxImageSemanticClassifier"]
            ),
        ),
        "image_description_backend": SimpleNamespace(
            SmolVlmDescriber=classes["SmolVlmDescriber"],
        ),
        "image_semantic_merge": SimpleNamespace(
            MergedImageSemanticClassifier=(
                classes["MergedImageSemanticClassifier"]
            ),
        ),
        "paddleocr_v3_backend": SimpleNamespace(
            PaddleOcrV3Backend=classes["PaddleOcrV3Backend"],
        ),
        "paddle_structure_v3_backend": SimpleNamespace(
            PaddleStructureV3Backend=(
                classes["PaddleStructureV3Backend"]
            ),
        ),
        "pdf_paddle_backend": SimpleNamespace(
            PaddlePdfSecondaryBackend=(
                classes["PaddlePdfSecondaryBackend"]
            ),
        ),
    }


def _settings(root: Path) -> ModelSettings:
    files = {}
    classifier = "docling-project--DocumentFigureClassifier-v2.5"
    for name in (
        f"{classifier}/model.onnx",
        f"{classifier}/config.json",
        f"{classifier}/preprocessor_config.json",
    ):
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(b"x")
        files[name] = "a" * 64
    rapid = {"det": "rapid/det.onnx"}
    paddle = {
        "text_detection_model_dir": root / "paddle/det",
        "text_recognition_model_dir": root / "paddle/rec",
    }
    return ModelSettings(root, files, rapid, paddle, root / "smol")


def test_build_vision_wires_v6_secondary_and_smol_merge(
    tmp_path: Path,
) -> None:
    modules = _modules()
    primary, semantic, secondary = build_vision(
        modules,  # type: ignore[arg-type]
        _settings(tmp_path),
    )
    assert type(primary).__name__ == "RapidOcrBackend"
    assert type(semantic).__name__ == "MergedImageSemanticClassifier"
    assert type(secondary).__name__ == "PaddleOcrV3Backend"
    calls = type(secondary).calls
    assert calls[0][0][0]["text_detection_model_dir"].name == "det"
    assert calls[0][1]["params"]["ocr_version"] == "PP-OCRv6"
    assert calls[0][1]["params"]["device"] == "cpu"
    assert calls[0][1]["params"]["enable_hpi"] is False
    assert calls[0][1]["params"]["enable_mkldnn"] is False
    assert calls[0][1]["params"]["precision"] == "fp32"


def test_missing_manifest_leaves_only_rapid_not_configured() -> None:
    modules = _modules()
    primary, semantic, secondary = build_vision(
        modules,  # type: ignore[arg-type]
        None,
    )
    assert type(primary).__name__ == "RapidOcrBackend"
    assert semantic is None
    assert secondary is None


def test_build_pdf_secondary_wires_local_v6_and_structure(
    tmp_path: Path,
) -> None:
    modules = _modules()
    settings = _settings(tmp_path)
    structure = {"layout_detection_model_dir": tmp_path / "layout"}
    settings = ModelSettings(
        settings.root,
        settings.files,
        settings.rapidocr,
        settings.paddleocr,
        settings.smolvlm,
        structure,
    )
    backend = build_pdf_secondary(
        modules,  # type: ignore[arg-type]
        settings,
    )
    assert type(backend).__name__ == "PaddlePdfSecondaryBackend"
    args, kwargs = type(backend).calls[0]
    assert type(args[0]).__name__ == "PaddleOcrV3Backend"
    assert kwargs["structure_backend"].__class__.__name__ == (
        "PaddleStructureV3Backend"
    )
    assert type(args[0]).calls[0][1]["params"]["ocr_version"] == (
        "PP-OCRv6"
    )
    assert type(args[0]).calls[0][1]["params"]["enable_mkldnn"] is False
    assert type(args[0]).calls[0][1]["params"]["enable_hpi"] is False
    assert type(args[0]).calls[0][1]["params"]["precision"] == "fp32"


def test_pdf_secondary_without_manifest_is_not_built() -> None:
    assert build_pdf_secondary(
        _modules(),  # type: ignore[arg-type]
        None,
    ) is None
