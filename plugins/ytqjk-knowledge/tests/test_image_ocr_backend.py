from __future__ import annotations

import sys
from pathlib import Path
from types import SimpleNamespace

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.image_ocr_backend import (  # noqa: E402
    OcrBackendError,
    OcrBackendResult,
    OcrEngineEvidence,
    OcrNotConfigured,
    OcrPoint,
    OcrTextBlock,
    RapidOcrBackend,
)


DIGEST_A = "a" * 64
DIGEST_B = "b" * 64


def _models(root: Path) -> dict[str, Path]:
    paths = {}
    for name in ("det", "cls", "rec", "rec_keys"):
        path = root / f"{name}.model"
        path.write_bytes(name.encode("ascii"))
        paths[name] = path
    return paths


def _output() -> SimpleNamespace:
    return SimpleNamespace(
        img=SimpleNamespace(shape=(100, 200, 3)),
        boxes=(
            ((10, 20), (80, 20), (80, 40), (10, 40)),
            ((10, 50), (90, 50), (90, 70), (10, 70)),
        ),
        txts=("第一行", "second"),
        scores=(0.98, 0.91),
        elapse=0.125,
    )


def _evidence() -> OcrEngineEvidence:
    return OcrEngineEvidence("fake", "1", DIGEST_A, DIGEST_B)


class EmptyRapidOutput:
    """Shape of RapidOCR 3.9.2 output when no text survives."""

    img = None
    boxes = None
    txts = None
    scores = None
    elapse = None


class EmptyRapidEngine:
    def __init__(self) -> None:
        self.decoded = SimpleNamespace(shape=(120, 240, 3))
        self.inference_input: object | None = None

    def load_img(self, value: bytes) -> object:
        assert value == b"blank-image"
        return self.decoded

    def __call__(self, value: object) -> EmptyRapidOutput:
        self.inference_input = value
        return EmptyRapidOutput()


def test_rapidocr_adapter_is_lazy_and_uses_explicit_models(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    created: list[dict[str, object]] = []

    def factory(*, params: dict[str, object]):
        created.append(params)
        return lambda image: _output()

    backend = RapidOcrBackend(
        _models(tmp_path),
        params={"Global.text_score": 0.5},
        engine_factory=factory,
    )
    assert created == []
    monkeypatch.setattr(backend, "_rapidocr_version", lambda: "3.9.2")
    result = backend.recognize(b"encoded-image")
    repeated = backend.recognize(b"encoded-image")
    assert len(created) == 1
    assert created[0]["Global.text_score"] == 0.5
    assert set(created[0]) >= {
        "Det.model_path",
        "Cls.model_path",
        "Rec.model_path",
        "Rec.rec_keys_path",
    }
    assert (result.width, result.height, result.elapsed_ms) == (200, 100, 125)
    assert result.blocks[0].text == "第一行"
    assert result.blocks[0].quad[2] == OcrPoint(80, 40)
    assert result.blocks[0].confidence == 0.98
    assert repeated.evidence == result.evidence
    assert result.evidence.package_version == "3.9.2"
    assert result.evidence.model_digest != DIGEST_A


def test_missing_model_is_not_configured_before_engine_load(
    tmp_path: Path,
) -> None:
    called = False

    def factory(**kwargs: object) -> object:
        nonlocal called
        called = True
        return object()

    models = _models(tmp_path)
    models["rec"].unlink()
    backend = RapidOcrBackend(models, engine_factory=factory)
    with pytest.raises(OcrNotConfigured, match="NOT_CONFIGURED.*rec"):
        backend.recognize(b"image")
    assert called is False


def test_incomplete_model_set_is_not_configured() -> None:
    backend = RapidOcrBackend({})
    with pytest.raises(OcrNotConfigured, match="det, cls, rec"):
        backend.recognize(b"image")


def test_wrong_package_version_is_not_configured(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    backend = RapidOcrBackend(_models(tmp_path), engine_factory=lambda: None)
    monkeypatch.setattr(
        "scripts.image_ocr_backend.importlib.metadata.version",
        lambda name: "3.9.1",
    )
    with pytest.raises(OcrNotConfigured, match="version must be 3.9.2"):
        backend.recognize(b"image")


@pytest.mark.parametrize(
    ("change", "message"),
    (
        ({"img": SimpleNamespace(shape=(0, 20, 3))}, "height is invalid"),
        ({"txts": ("only",)}, "lengths do not match"),
        ({"scores": (1.1, 0.9)}, "confidence must be"),
        ({"boxes": (((1, 2),), ((1, 2),))}, "four points"),
        ({"txts": (None, "valid")}, "text must be text"),
    ),
)
def test_adapter_rejects_malformed_output(
    change: dict[str, object],
    message: str,
) -> None:
    values = vars(_output()) | change
    with pytest.raises(OcrBackendError, match=message):
        RapidOcrBackend._convert_output(SimpleNamespace(**values), _evidence())


def test_adapter_accepts_no_text_output() -> None:
    output = _output()
    output.boxes = None
    output.txts = None
    output.scores = None
    result = RapidOcrBackend._convert_output(output, _evidence())
    assert result.blocks == ()


def test_official_empty_output_uses_predecoded_image_shape(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    engine = EmptyRapidEngine()

    def factory(**kwargs: object) -> EmptyRapidEngine:
        return engine

    backend = RapidOcrBackend(_models(tmp_path), engine_factory=factory)
    monkeypatch.setattr(backend, "_rapidocr_version", lambda: "3.9.2")
    result = backend.recognize(b"blank-image")
    assert (result.width, result.height) == (240, 120)
    assert result.blocks == ()
    assert result.elapsed_ms == 0
    assert engine.inference_input is engine.decoded


def test_empty_output_without_trusted_shape_fails_closed() -> None:
    with pytest.raises(OcrBackendError, match="no image dimensions"):
        RapidOcrBackend._convert_output(EmptyRapidOutput(), _evidence())


@pytest.mark.parametrize(
    "build",
    (
        lambda: OcrPoint(float("nan"), 0),
        lambda: OcrPoint(-1, 0),
        lambda: OcrTextBlock("text", (OcrPoint(0, 0),) * 3, 0.9),
        lambda: OcrTextBlock("text", (OcrPoint(0, 0),) * 4, True),
        lambda: OcrEngineEvidence("", "1", DIGEST_A, DIGEST_B),
        lambda: OcrBackendResult(0, 2, (), 0, _evidence()),
    ),
)
def test_backend_contracts_fail_closed(build: object) -> None:
    with pytest.raises(ValueError):
        build()


def test_config_evidence_is_stable_and_secret_path_free(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    paths = _models(tmp_path)

    def factory(**kwargs: object):
        return lambda image: _output()

    first = RapidOcrBackend(paths, engine_factory=factory)
    second = RapidOcrBackend(paths, engine_factory=factory)
    monkeypatch.setattr(first, "_rapidocr_version", lambda: "3.9.2")
    monkeypatch.setattr(second, "_rapidocr_version", lambda: "3.9.2")
    first_result = first.recognize(b"image")
    second_result = second.recognize(b"image")
    assert first_result.evidence == second_result.evidence
    assert str(tmp_path) not in first_result.evidence.config_digest


def test_parameters_reject_non_scalar_and_non_finite_values() -> None:
    with pytest.raises(ValueError, match="scalar"):
        RapidOcrBackend({}, params={"unsafe": object()})
    with pytest.raises(ValueError, match="finite"):
        RapidOcrBackend({}, params={"unsafe": float("inf")})
