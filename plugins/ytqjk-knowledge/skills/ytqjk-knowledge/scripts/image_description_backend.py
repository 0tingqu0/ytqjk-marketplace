"""Offline SmolVLM description with bounded, searchable output."""

from __future__ import annotations

import hashlib
import importlib.metadata
import json
import re
import time
from dataclasses import dataclass
from io import BytesIO
from pathlib import Path
from typing import Callable

from PIL import Image, ImageOps, UnidentifiedImageError

from scripts.artifact_safety import (
    ArtifactSafetyError,
    TreeGuard,
    snapshot_tree,
    verify_tree,
)

from scripts.image_ocr_backend import OcrNotConfigured
from scripts.intake_extraction_contracts import RecognitionEvidence
from scripts.intake_security import LocalScanner


MODEL_NAME = "SmolVLM-256M-Instruct"
ENGINE_NAME = "smolvlm-local"
MAX_IMAGE_BYTES = 50 * 1024 * 1024
MAX_IMAGE_PIXELS = 20_000_000
MAX_MODEL_FILES = 256
MAX_MODEL_BYTES = 2 * 1024 * 1024 * 1024
MAX_OUTPUT_CHARACTERS = 8192
MAX_NEW_TOKENS = 160
PROMPT = (
    "Return only strict JSON with keys description and tags. "
    "description: one factual English sentence about visible content; "
    "tags: 1 to 8 short lowercase search terms. Do not infer identity, "
    "intent, private data, or facts not visible."
)
_PATH = re.compile(
    r"(?:[A-Za-z]:[\\/]|\\\\|(?<![\w:])/(?:home|users|var|etc|tmp|opt|root)/)",
    re.IGNORECASE,
)
_SCANNER = LocalScanner()
Generator = Callable[[Path, Image.Image, int], str]


@dataclass(frozen=True)
class ImageDescription:
    summary: str
    tags: tuple[str, ...]
    elapsed_ms: int
    evidence: RecognitionEvidence


class SmolVlmDescriber:
    """Use an explicitly local model directory; never resolve remotely."""

    def __init__(
        self,
        model_dir: str | Path,
        *,
        generator: Generator | None = None,
        version_getter: Callable[[], str] | None = None,
    ) -> None:
        self._model_dir = Path(model_dir)
        self._generator = generator
        self._version_getter = version_getter or _transformers_version
        self._loaded: (
            tuple[Path, RecognitionEvidence, TreeGuard] | None
        ) = None

    def describe(self, image_bytes: bytes) -> ImageDescription:
        image = _decode_image(image_bytes)
        root, evidence, guard = self._load()
        self._verify(guard, False)
        generator = self._generator or _transformers_generate
        started = time.perf_counter()
        try:
            raw = generator(root, image, MAX_NEW_TOKENS)
            self._verify(guard, False)
        except OcrNotConfigured:
            raise
        except ValueError:
            raise
        except Exception as error:
            raise ValueError("image description inference failed") from error
        elapsed = round((time.perf_counter() - started) * 1000)
        summary, tags = parse_description_output(raw)
        return ImageDescription(summary, tags, elapsed, evidence)

    def _load(self) -> tuple[Path, RecognitionEvidence, TreeGuard]:
        if self._loaded is not None:
            self._verify(self._loaded[2], True)
            return self._loaded
        root, tree, guard = _model_inventory(self._model_dir)
        version = self._version_getter()
        if not isinstance(version, str) or not version.strip():
            raise OcrNotConfigured(
                "NOT_CONFIGURED: transformers version is invalid"
            )
        config = _digest({
            "max_new_tokens": MAX_NEW_TOKENS,
            "model_tree": tree,
            "prompt": PROMPT,
            "transformers": version,
        })
        evidence = RecognitionEvidence(
            ENGINE_NAME,
            f"{MODEL_NAME}+transformers-{version}",
            config,
        )
        self._verify(guard, True)
        self._loaded = root, evidence, guard
        return self._loaded

    @staticmethod
    def _verify(guard: TreeGuard, loading: bool) -> None:
        try:
            verify_tree(guard)
        except ArtifactSafetyError as error:
            if loading:
                raise OcrNotConfigured(
                    "NOT_CONFIGURED: SmolVLM artifacts changed"
                ) from error
            raise ValueError(
                "SmolVLM artifacts changed during inference"
            ) from error


def _transformers_version() -> str:
    try:
        return importlib.metadata.version("transformers")
    except importlib.metadata.PackageNotFoundError as error:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: transformers is unavailable"
        ) from error


def _transformers_generate(
    root: Path,
    image: Image.Image,
    max_tokens: int,
) -> str:
    try:
        import torch
        from transformers import AutoModelForImageTextToText, AutoProcessor
    except (ImportError, ModuleNotFoundError) as error:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: SmolVLM runtime is unavailable"
        ) from error
    device = "cuda" if torch.cuda.is_available() else "cpu"
    dtype = torch.float16 if device == "cuda" else torch.float32
    try:
        processor = AutoProcessor.from_pretrained(
            root,
            local_files_only=True,
            trust_remote_code=False,
        )
        model = AutoModelForImageTextToText.from_pretrained(
            root,
            local_files_only=True,
            trust_remote_code=False,
            torch_dtype=dtype,
        ).to(device)
        model.eval()
        messages = [{
            "role": "user",
            "content": [
                {"type": "image"},
                {"type": "text", "text": PROMPT},
            ],
        }]
        prompt = processor.apply_chat_template(
            messages,
            add_generation_prompt=True,
        )
        inputs = processor(
            text=prompt,
            images=[image],
            return_tensors="pt",
        ).to(device)
        with torch.inference_mode():
            output = model.generate(
                **inputs,
                do_sample=False,
                max_new_tokens=max_tokens,
            )
        prefix = inputs["input_ids"].shape[-1]
        return processor.decode(
            output[0][prefix:],
            skip_special_tokens=True,
        )
    except OcrNotConfigured:
        raise
    except Exception as error:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: local SmolVLM model cannot load"
        ) from error


def _decode_image(value: bytes) -> Image.Image:
    if not isinstance(value, bytes) or not value:
        raise ValueError("image input must be non-empty bytes")
    if len(value) > MAX_IMAGE_BYTES:
        raise ValueError("image byte limit exceeded")
    try:
        with Image.open(BytesIO(value)) as opened:
            if opened.width * opened.height > MAX_IMAGE_PIXELS:
                raise ValueError("image pixel limit exceeded")
            image = ImageOps.exif_transpose(opened)
            if image.width * image.height > MAX_IMAGE_PIXELS:
                raise ValueError("image pixel limit exceeded")
            return image.convert("RGB").copy()
    except (UnidentifiedImageError, OSError) as error:
        raise ValueError("image decoding failed") from error


def parse_description_output(
    value: object,
) -> tuple[str, tuple[str, ...]]:
    if not isinstance(value, str) or len(value) > MAX_OUTPUT_CHARACTERS:
        raise ValueError("image description output is invalid")
    text = value.strip()
    if not text.startswith("{") or not text.endswith("}"):
        raise ValueError("image description output is invalid")
    try:
        payload = json.loads(
            text,
            parse_constant=lambda item: (_ for _ in ()).throw(
                ValueError(item)
            ),
        )
    except (json.JSONDecodeError, ValueError) as error:
        raise ValueError("image description output is invalid") from error
    if type(payload) is not dict or set(payload) != {"description", "tags"}:
        raise ValueError("image description output is invalid")
    summary = _safe_text(payload["description"], "description", 1500)
    raw_tags = payload["tags"]
    if type(raw_tags) is not list or not 1 <= len(raw_tags) <= 8:
        raise ValueError("image description tags are invalid")
    tags = tuple(_safe_text(item, "tag", 48) for item in raw_tags)
    if len(set(tags)) != len(tags):
        raise ValueError("image description tags are invalid")
    return summary, tags


def _safe_text(value: object, field: str, limit: int) -> str:
    if type(value) is not str or not value.strip() or len(value) > limit:
        raise ValueError(f"image {field} is invalid")
    text = " ".join(value.split())
    if _PATH.search(text):
        raise ValueError(f"image {field} contains a path")
    result = _SCANNER.scan(text.encode("utf-8"), "image-description")
    if result.state.value != "CLEAN":
        raise ValueError(f"image {field} contains a secret")
    return text


def _model_inventory(value: Path) -> tuple[Path, str, TreeGuard]:
    try:
        root = value.resolve(strict=True)
    except OSError as error:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: local SmolVLM model is missing"
        ) from error
    try:
        guard = snapshot_tree(root, MAX_MODEL_FILES, MAX_MODEL_BYTES)
    except ArtifactSafetyError as error:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: local SmolVLM model is unsafe"
        ) from error
    files = guard.hashes
    required = "config.json" in files and "model.safetensors" in files
    if not required:
        raise OcrNotConfigured(
            "NOT_CONFIGURED: local SmolVLM model is incomplete"
        )
    return root, _digest(files), guard


def _digest(value: object) -> str:
    data = json.dumps(
        value,
        ensure_ascii=False,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(data).hexdigest()


__all__ = [
    "ImageDescription",
    "SmolVlmDescriber",
    "parse_description_output",
]
