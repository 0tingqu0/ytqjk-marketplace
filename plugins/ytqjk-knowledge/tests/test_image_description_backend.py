from __future__ import annotations

import json
import sys
from io import BytesIO
from pathlib import Path

import pytest
from PIL import Image


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.image_description_backend import (  # noqa: E402
    ImageDescription,
    SmolVlmDescriber,
)
from scripts.image_ocr_backend import OcrNotConfigured  # noqa: E402


def _model(root: Path) -> Path:
    root.mkdir()
    (root / "config.json").write_text("{}", encoding="utf-8")
    (root / "model.safetensors").write_bytes(b"official-model")
    (root / "tokenizer.json").write_text("{}", encoding="utf-8")
    return root


def _image(width: int = 32, height: int = 24) -> bytes:
    stream = BytesIO()
    Image.new("RGB", (width, height), "white").save(stream, format="PNG")
    return stream.getvalue()


def test_local_description_is_bounded_searchable_and_evidenced(
    tmp_path: Path,
) -> None:
    seen: list[tuple[Path, tuple[int, int], int]] = []

    def generate(root: Path, image: Image.Image, tokens: int) -> str:
        seen.append((root, image.size, tokens))
        return json.dumps({
            "description": "A control diagram links intake to review.",
            "tags": ["control-diagram", "review"],
        })

    model = _model(tmp_path / "model")
    result = SmolVlmDescriber(
        model,
        generator=generate,
        version_getter=lambda: "5.15.1",
    ).describe(_image())
    assert type(result) is ImageDescription
    assert result.summary.startswith("A control diagram")
    assert result.tags == ("control-diagram", "review")
    assert result.elapsed_ms >= 0
    assert result.evidence.engine == "smolvlm-local"
    assert result.evidence.model_version.endswith("transformers-5.15.1")
    assert str(tmp_path) not in repr(result)
    assert seen[0][0] == model.resolve()
    assert seen[0][1] == (32, 24)
    assert seen[0][2] == 160


@pytest.mark.parametrize(
    "output",
    (
        "not json",
        'secret-before {"description":"ok","tags":["x"]}',
        '{"description":"x","tags":[NaN]}',
        json.dumps({"description": r"C:\\private\\file", "tags": ["x"]}),
        json.dumps({"description": "api_key=" + "a" * 24, "tags": ["x"]}),
        json.dumps({"description": "ok", "tags": ["same", "same"]}),
        json.dumps({"description": "ok", "tags": []}),
    ),
)
def test_unsafe_or_malformed_output_fails_closed(
    tmp_path: Path,
    output: str,
) -> None:
    backend = SmolVlmDescriber(
        _model(tmp_path / "model"),
        generator=lambda *_args: output,
        version_getter=lambda: "5.15.1",
    )
    with pytest.raises(ValueError, match="image"):
        backend.describe(_image())


def test_missing_or_linked_model_is_not_configured(tmp_path: Path) -> None:
    missing = SmolVlmDescriber(
        tmp_path / "missing",
        generator=lambda *_args: "{}",
        version_getter=lambda: "5.15.1",
    )
    with pytest.raises(OcrNotConfigured, match="NOT_CONFIGURED"):
        missing.describe(_image())


def test_model_digest_changes_with_local_artifact(tmp_path: Path) -> None:
    model = _model(tmp_path / "model")
    output = json.dumps({"description": "A page.", "tags": ["page"]})
    first = SmolVlmDescriber(
        model,
        generator=lambda *_args: output,
        version_getter=lambda: "5.15.1",
    ).describe(_image())
    (model / "tokenizer.json").write_text('{"changed":true}', encoding="utf-8")
    second = SmolVlmDescriber(
        model,
        generator=lambda *_args: output,
        version_getter=lambda: "5.15.1",
    ).describe(_image())
    assert first.evidence.config_digest != second.evidence.config_digest
