from __future__ import annotations

import json
import sys
from io import BytesIO
from pathlib import Path
from types import SimpleNamespace

import numpy as np
import pytest
from PIL import Image


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.image_document_extractor import ImageFeatures  # noqa: E402
from scripts.image_ocr_backend import OcrNotConfigured  # noqa: E402
from scripts.image_semantic_backend import (  # noqa: E402
    MODEL_NAME,
    OnnxImageSemanticClassifier,
)


LABELS = (
    "logo",
    "photograph",
    "icon",
    "engineering_drawing",
    "line_chart",
    "bar_chart",
    "other",
    "table",
    "flow_chart",
    "screenshot_from_computer",
    "signature",
    "screenshot_from_manual",
    "geographical_map",
    "pie_chart",
    "page_thumbnail",
    "stamp",
    "music",
    "calendar",
    "qr_code",
    "bar_code",
    "full_page_image",
    "scatter_plot",
    "chemistry_structure",
    "topographical_map",
    "crossword_puzzle",
    "box_plot",
)


def _artifacts(root: Path) -> dict[str, Path]:
    model = root / "model.onnx"
    config = root / "config.json"
    preprocessor = root / "preprocessor_config.json"
    model.write_bytes(b"official-onnx-model")
    config.write_text(
        json.dumps({"id2label": dict(enumerate(LABELS))}),
        encoding="utf-8",
    )
    preprocessor.write_text(
        json.dumps({
            "do_normalize": True,
            "do_rescale": True,
            "do_resize": True,
            "image_mean": [0.485, 0.456, 0.406],
            "image_std": [0.47853944, 0.4732864, 0.47434163],
            "rescale_factor": 1 / 255,
            "size": {"height": 224, "width": 224},
        }),
        encoding="utf-8",
    )
    return {"model": model, "config": config, "preprocessor": preprocessor}


def _image() -> bytes:
    stream = BytesIO()
    Image.new("RGB", (32, 24), "white").save(stream, format="PNG")
    return stream.getvalue()


def _features() -> ImageFeatures:
    return ImageFeatures("plain.png", 0, 0, 0, 0, "")


class FakeSession:
    def __init__(self, logits: np.ndarray) -> None:
        self.logits = logits
        self.inputs: list[np.ndarray] = []

    @staticmethod
    def get_inputs() -> list[object]:
        return [SimpleNamespace(name="pixel_values")]

    def run(
        self, outputs: object, values: dict[str, np.ndarray],
    ) -> list[np.ndarray]:
        assert outputs is None
        self.inputs.append(values["pixel_values"])
        return [self.logits]


def _classifier(
    root: Path,
    logits: np.ndarray,
) -> tuple[OnnxImageSemanticClassifier, FakeSession]:
    session = FakeSession(logits)
    classifier = OnnxImageSemanticClassifier(
        _artifacts(root),
        session_factory=lambda *_args, **_kwargs: session,
        runtime_version="test-runtime",
    )
    return classifier, session


def test_classifier_uses_pixels_and_returns_searchable_semantics(
    tmp_path: Path,
) -> None:
    logits = np.zeros((1, len(LABELS)), dtype=np.float32)
    logits[0, LABELS.index("photograph")] = 8
    classifier, session = _classifier(tmp_path, logits)
    result = classifier.classify(_image(), _features())
    assert result.category == "photo"
    assert result.confidence > 0.98
    assert "photograph" in result.tags
    assert "照片" in result.summary
    assert result.evidence.engine == "docling-picture-classifier-onnx"
    assert result.evidence.model_version.startswith(MODEL_NAME)
    assert session.inputs[0].shape == (1, 3, 224, 224)
    assert session.inputs[0].dtype == np.float32
    assert str(tmp_path) not in result.evidence.config_digest


def test_uncertain_prediction_is_unknown_and_requires_review_upstream(
    tmp_path: Path,
) -> None:
    logits = np.zeros((1, len(LABELS)), dtype=np.float32)
    classifier, _session = _classifier(tmp_path, logits)
    result = classifier.classify(_image(), _features())
    assert result.category == "unknown"
    assert result.confidence == pytest.approx(1 / len(LABELS))
    assert "other" in result.tags


@pytest.mark.parametrize(
    "logits",
    (
        np.zeros((2, len(LABELS)), dtype=np.float32),
        np.zeros((1, len(LABELS) - 1), dtype=np.float32),
        np.full((1, len(LABELS)), np.nan, dtype=np.float32),
    ),
)
def test_malformed_runtime_output_fails_closed(
    tmp_path: Path,
    logits: np.ndarray,
) -> None:
    classifier, _session = _classifier(tmp_path, logits)
    with pytest.raises(ValueError, match="classifier output"):
        classifier.classify(_image(), _features())


def test_missing_artifact_is_explicitly_not_configured(
    tmp_path: Path,
) -> None:
    files = _artifacts(tmp_path)
    files["model"].unlink()
    classifier = OnnxImageSemanticClassifier(
        files,
        session_factory=lambda *_args, **_kwargs: object(),
        runtime_version="test-runtime",
    )
    with pytest.raises(OcrNotConfigured, match="NOT_CONFIGURED"):
        classifier.classify(_image(), _features())


def test_unexpected_label_contract_is_rejected_before_inference(
    tmp_path: Path,
) -> None:
    files = _artifacts(tmp_path)
    files["config"].write_text(
        json.dumps({"id2label": {"0": "secret/new-label"}}),
        encoding="utf-8",
    )
    called = False

    def factory(*_args: object, **_kwargs: object) -> object:
        nonlocal called
        called = True
        return object()

    classifier = OnnxImageSemanticClassifier(
        files,
        session_factory=factory,
        runtime_version="test-runtime",
    )
    with pytest.raises(OcrNotConfigured, match="label contract"):
        classifier.classify(_image(), _features())
    assert called is False
