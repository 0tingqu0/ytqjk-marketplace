"""Materialize version-pinned RapidOCR assets without model auto-download."""

from __future__ import annotations

import argparse
import hashlib
import importlib.resources
import os
import shutil
import urllib.request
import uuid
from pathlib import Path

from document_runtime_downloads import windows_download_layout


RAPIDOCR_FILES = {
    "PP-OCRv6_det_small.onnx": (
        "RapidOcr/onnx/det/ch_PP-OCRv6_det_infer.onnx"
    ),
    "ch_ppocr_mobile_v2.0_cls_mobile.onnx": (
        "RapidOcr/onnx/cls/ch_PP-OCRv4_cls_infer.onnx"
    ),
    "PP-OCRv6_rec_small.onnx": (
        "RapidOcr/onnx/rec/ch_PP-OCRv6_rec_infer.onnx"
    ),
}
RAPIDOCR_DICT = (
    "https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/"
    "v3.9.2/paddle/PP-OCRv6/rec/PP-OCRv6_rec_small/"
    "ppocrv6_dict.txt"
)
RAPIDOCR_DICT_SHA256 = (
    "b5f2bfe2bdd9448429e3e82b51c789775d9b42f2403d082b00662eb77e401c5d"
)
RAPIDOCR_DICT_TARGET = "RapidOcr/onnx/rec/ch_ppocrv6_keys.txt"
MAX_DICT_BYTES = 1024 * 1024


class RuntimeAssetError(RuntimeError):
    """Pinned package or remote asset failed verification."""


def materialize(output: Path) -> None:
    root = output.resolve()
    root.mkdir(parents=True, exist_ok=True)
    models = importlib.resources.files("rapidocr").joinpath("models")
    for source_name, target_name in RAPIDOCR_FILES.items():
        source = models.joinpath(source_name)
        try:
            content = source.read_bytes()
        except (FileNotFoundError, OSError) as error:
            raise RuntimeAssetError("RAPIDOCR_PACKAGE_ASSET_MISSING") from error
        if not content:
            raise RuntimeAssetError("RAPIDOCR_PACKAGE_ASSET_INVALID")
        _write(root / target_name, content)
    _write(root / RAPIDOCR_DICT_TARGET, _download_dictionary())


def normalize_windows_downloads(output: Path) -> None:
    root = output.resolve()
    for source_name, target_name in windows_download_layout():
        source = root / source_name
        target = root / target_name
        if not source.is_dir() or _is_link(source) or target.exists():
            raise RuntimeAssetError("WINDOWS_MODEL_LAYOUT_INVALID")
        _remove_download_cache(root, source)
        target.parent.mkdir(parents=True, exist_ok=True)
        if _is_link(target.parent):
            raise RuntimeAssetError("WINDOWS_MODEL_LAYOUT_UNSAFE")
        os.replace(source, target)
    short_root = root / ".w"
    try:
        short_root.rmdir()
    except OSError as error:
        raise RuntimeAssetError("WINDOWS_MODEL_LAYOUT_DIRTY") from error


def _remove_download_cache(root: Path, source: Path) -> None:
    cache = source / ".cache"
    if not cache.exists():
        return
    try:
        cache.resolve(strict=True).relative_to(root.resolve(strict=True))
    except (OSError, ValueError) as error:
        raise RuntimeAssetError("WINDOWS_MODEL_CACHE_ESCAPE") from error
    paths = (cache, *cache.rglob("*"))
    if not cache.is_dir() or any(_is_link(path) for path in paths):
        raise RuntimeAssetError("WINDOWS_MODEL_CACHE_UNSAFE")
    shutil.rmtree(cache)


def _is_link(path: Path) -> bool:
    junction = getattr(path, "is_junction", lambda: False)()
    return path.is_symlink() or junction


def _download_dictionary() -> bytes:
    try:
        with urllib.request.urlopen(RAPIDOCR_DICT, timeout=120) as response:
            content = response.read(MAX_DICT_BYTES + 1)
    except (OSError, TimeoutError) as error:
        raise RuntimeAssetError("RAPIDOCR_DICTIONARY_UNAVAILABLE") from error
    if not content or len(content) > MAX_DICT_BYTES:
        raise RuntimeAssetError("RAPIDOCR_DICTIONARY_INVALID")
    if hashlib.sha256(content).hexdigest() != RAPIDOCR_DICT_SHA256:
        raise RuntimeAssetError("RAPIDOCR_DICTIONARY_DIGEST_MISMATCH")
    return content


def _write(path: Path, content: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    try:
        temporary.write_bytes(content)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--windows-layout", action="store_true")
    arguments = parser.parse_args()
    if arguments.windows_layout:
        normalize_windows_downloads(arguments.output)
    materialize(arguments.output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
