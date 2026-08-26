from __future__ import annotations

import sys
from pathlib import Path
from types import SimpleNamespace

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.image_ocr_backend import (  # noqa: E402
    OcrBackendError,
    OcrNotConfigured,
)
from scripts.paddleocr_v3_backend import (  # noqa: E402
    PaddleOcrV3Backend,
)


class Image:
    shape = (100, 200, 3)


class Result:
    def __init__(self, payload: object) -> None:
        self.json = payload


class Pipeline:
    def __init__(self, payload: object) -> None:
        self.payload = payload
        self.inputs: list[object] = []

    def predict(self, image: object) -> list[Result]:
        self.inputs.append(image)
        return [Result(self.payload)]


def _payload(
    texts: object = None,
    scores: object = None,
    polygons: object = None,
) -> dict[str, object]:
    return {
        "res": {
            "rec_texts": ["中文识别"] if texts is None else texts,
            "rec_scores": [0.97] if scores is None else scores,
            "rec_polys": [
                [[10, 10], [190, 10], [190, 30], [10, 30]]
            ] if polygons is None else polygons,
        }
    }


def _models(tmp_path: Path) -> dict[str, Path]:
    detection = tmp_path / "det"
    recognition = tmp_path / "rec"
    detection.mkdir(parents=True)
    recognition.mkdir(parents=True)
    (detection / "model.json").write_text("det", encoding="utf-8")
    (recognition / "model.json").write_text("rec", encoding="utf-8")
    return {
        "text_detection_model_dir": detection,
        "text_recognition_model_dir": recognition,
    }


def _backend(
    tmp_path: Path,
    payload: object,
    **overrides: object,
) -> tuple[PaddleOcrV3Backend, Pipeline, dict[str, object]]:
    pipeline = Pipeline(payload)
    factory_args: dict[str, object] = {}

    def factory(**kwargs: object) -> Pipeline:
        factory_args.update(kwargs)
        return pipeline

    values = {
        "pipeline_factory": factory,
        "image_decoder": lambda value: Image(),
        "version_getter": lambda: "3.3.2",
    }
    values.update(overrides)
    backend = PaddleOcrV3Backend(
        _models(tmp_path),
        params={"device": "cpu"},
        **values,
    )
    return backend, pipeline, factory_args


def test_documented_v3_json_maps_to_safe_backend_result(
    tmp_path: Path,
) -> None:
    backend, pipeline, factory_args = _backend(tmp_path, _payload())
    result = backend.recognize(b"image")
    assert result.width == 200
    assert result.height == 100
    assert result.blocks[0].text == "中文识别"
    assert result.blocks[0].confidence == 0.97
    assert result.blocks[0].quad[2].x == 190
    assert result.evidence.engine == "paddleocr-3"
    assert result.evidence.package_version == "3.3.2"
    assert pipeline.inputs and isinstance(pipeline.inputs[0], Image)
    assert factory_args["device"] == "cpu"
    assert Path(
        factory_args["text_detection_model_dir"]  # type: ignore[arg-type]
    ).is_absolute()


def test_blank_official_text_entry_is_skipped_with_alignment(
    tmp_path: Path,
) -> None:
    payload = _payload(
        ["", "有效文本"],
        [0.20, 0.99],
        [
            [[1, 1], [2, 1], [2, 2], [1, 2]],
            [[10, 10], [90, 10], [90, 30], [10, 30]],
        ],
    )
    backend, _, _ = _backend(tmp_path, payload)
    result = backend.recognize(b"image")
    assert [block.text for block in result.blocks] == ["有效文本"]


def test_model_digest_and_config_digest_are_stable(tmp_path: Path) -> None:
    first, _, _ = _backend(tmp_path / "one", _payload())
    second, _, _ = _backend(tmp_path / "two", _payload())
    first_result = first.recognize(b"image")
    second_result = second.recognize(b"image")
    assert first_result.evidence.model_digest == (
        second_result.evidence.model_digest
    )
    assert first_result.evidence.config_digest == (
        second_result.evidence.config_digest
    )


def test_missing_models_and_wrong_major_are_not_configured(
    tmp_path: Path,
) -> None:
    missing = PaddleOcrV3Backend(
        {},
        pipeline_factory=lambda **kwargs: Pipeline(_payload()),
        image_decoder=lambda value: Image(),
        version_getter=lambda: "3.3.2",
    )
    with pytest.raises(OcrNotConfigured, match="det and rec"):
        missing.recognize(b"image")
    wrong, _, _ = _backend(
        tmp_path,
        _payload(),
        version_getter=lambda: "4.0.0",
    )
    with pytest.raises(OcrNotConfigured, match="major version"):
        wrong.recognize(b"image")


def test_missing_decoder_runtime_remains_not_configured(
    tmp_path: Path,
) -> None:
    def missing_decoder(value: bytes) -> object:
        raise OcrNotConfigured("NOT_CONFIGURED: image decoder unavailable")

    backend, _, _ = _backend(
        tmp_path,
        _payload(),
        image_decoder=missing_decoder,
    )
    with pytest.raises(OcrNotConfigured, match="decoder unavailable"):
        backend.recognize(b"image")


@pytest.mark.parametrize(
    "payload",
    (
        {"not_res": {}},
        _payload(texts=["one", "two"]),
        _payload(scores=[float("nan")]),
        _payload(polygons=[[[1, 1], [2, 1], [2, 2]]]),
        _payload(texts=[object()]),
    ),
)
def test_malformed_output_fails_closed(
    tmp_path: Path,
    payload: object,
) -> None:
    backend, _, _ = _backend(tmp_path, payload)
    with pytest.raises(OcrBackendError):
        backend.recognize(b"image")


def test_inference_error_redacts_exception_details(tmp_path: Path) -> None:
    class BrokenPipeline:
        def predict(self, image: object) -> object:
            raise RuntimeError(r"token=secret C:\private\model")

    backend = PaddleOcrV3Backend(
        _models(tmp_path),
        pipeline_factory=lambda **kwargs: BrokenPipeline(),
        image_decoder=lambda value: Image(),
        version_getter=lambda: "3.3.2",
    )
    with pytest.raises(OcrBackendError) as captured:
        backend.recognize(b"image")
    assert "secret" not in str(captured.value)
    assert "private" not in str(captured.value)


def test_unsupported_or_nonfinite_parameter_is_rejected(
    tmp_path: Path,
) -> None:
    models = _models(tmp_path)
    with pytest.raises(ValueError, match="unsupported"):
        PaddleOcrV3Backend(models, params={"unknown": True})
    with pytest.raises(ValueError, match="finite"):
        PaddleOcrV3Backend(
            models,
            params={"text_rec_score_thresh": float("inf")},
        )
