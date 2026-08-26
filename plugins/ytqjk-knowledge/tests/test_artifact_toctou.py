from __future__ import annotations

import json
import os
import sys
from io import BytesIO
from pathlib import Path
from types import SimpleNamespace

import pytest
from PIL import Image


SKILL = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL))

import scripts.artifact_safety as safety  # noqa: E402
from scripts.artifact_safety import ArtifactSafetyError  # noqa: E402
from scripts.image_description_backend import SmolVlmDescriber  # noqa: E402
from scripts.image_ocr_backend import OcrBackendError  # noqa: E402
from scripts.image_ocr_backend import RapidOcrBackend  # noqa: E402
from scripts.paddle_structure_v3_backend import (  # noqa: E402
    MODEL_KEYS,
    PaddleStructureV3Backend,
)


def _image() -> bytes:
    stream = BytesIO()
    Image.new("RGB", (8, 8), "white").save(stream, format="PNG")
    return stream.getvalue()


def test_artifact_hardlink_and_parent_reparse_are_rejected(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    parent = tmp_path / "models"
    parent.mkdir()
    source = parent / "model.bin"
    linked = parent / "linked.bin"
    source.write_bytes(b"model")
    os.link(source, linked)
    with pytest.raises(ArtifactSafetyError, match="SINGLE_LINK"):
        safety.snapshot_file(source, 1024)
    linked.unlink()
    original = safety._is_reparse
    monkeypatch.setattr(
        safety,
        "_is_reparse",
        lambda path: path == parent or original(path),
    )
    with pytest.raises(ArtifactSafetyError, match="REPARSE"):
        safety.snapshot_file(source, 1024)


def test_artifact_replacement_during_open_is_rejected(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    artifact = tmp_path / "model.bin"
    replacement = tmp_path / "replacement.bin"
    artifact.write_bytes(b"old")
    replacement.write_bytes(b"new")
    original = safety.os.open

    def replace_after_open(path: object, flags: int) -> int:
        descriptor = original(path, flags)
        os.replace(replacement, artifact)
        return descriptor

    monkeypatch.setattr(safety.os, "open", replace_after_open)
    with pytest.raises(
        ArtifactSafetyError,
        match="CHANGED|UNAVAILABLE",
    ):
        safety.snapshot_file(artifact, 1024)


def test_rapidocr_rejects_model_replaced_during_inference(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    models = {}
    for name in ("det", "cls", "rec", "rec_keys"):
        path = tmp_path / name
        path.write_bytes(name.encode())
        models[name] = path

    def engine(_value: object) -> object:
        replacement = tmp_path / "new"
        replacement.write_bytes(b"changed")
        os.replace(replacement, models["det"])
        return SimpleNamespace(
            img=SimpleNamespace(shape=(8, 8, 3)),
            boxes=None,
            txts=None,
            scores=None,
            elapse=0,
        )

    backend = RapidOcrBackend(models, engine_factory=lambda **_: engine)
    monkeypatch.setattr(backend, "_rapidocr_version", lambda: "3.9.2")
    with pytest.raises(OcrBackendError, match="changed"):
        backend.recognize(b"image")


def test_description_rejects_model_replaced_during_inference(
    tmp_path: Path,
) -> None:
    root = tmp_path / "model"
    root.mkdir()
    target = root / "model.safetensors"
    target.write_bytes(b"model")
    (root / "config.json").write_text("{}", encoding="utf-8")

    def generator(*_args: object) -> str:
        replacement = root / "replacement"
        replacement.write_bytes(b"changed")
        os.replace(replacement, target)
        return json.dumps({"description": "A page.", "tags": ["page"]})

    backend = SmolVlmDescriber(
        root,
        generator=generator,
        version_getter=lambda: "5.15.1",
    )
    with pytest.raises(ValueError, match="changed"):
        backend.describe(_image())


def test_structure_rejects_hardlink_and_inference_replacement(
    tmp_path: Path,
) -> None:
    models = {}
    targets = []
    for key in MODEL_KEYS:
        root = tmp_path / key
        root.mkdir()
        target = root / "model.bin"
        target.write_bytes(key.encode())
        targets.append(target)
        models[key] = root
    linked = targets[0].with_name("linked.bin")
    os.link(targets[0], linked)
    blocked = PaddleStructureV3Backend(
        models,
        pipeline_factory=lambda **_: object(),
        version_getter=lambda: "3.7.0",
    )
    with pytest.raises(Exception, match="NOT_CONFIGURED"):
        blocked.analyze(_image())
    linked.unlink()

    class Pipeline:
        def predict(self, _image_value: object) -> list[object]:
            replacement = targets[0].with_name("replacement.bin")
            replacement.write_bytes(b"changed")
            os.replace(replacement, targets[0])
            payload = {
                "res": {
                    "parsing_res_list": [{
                        "block_content": "text",
                        "block_bbox": [0, 0, 4, 4],
                        "block_score": 0.9,
                    }],
                },
            }
            return [SimpleNamespace(json=payload)]

    backend = PaddleStructureV3Backend(
        models,
        pipeline_factory=lambda **_: Pipeline(),
        version_getter=lambda: "3.7.0",
    )
    with pytest.raises(OcrBackendError, match="changed"):
        backend.analyze(_image())
