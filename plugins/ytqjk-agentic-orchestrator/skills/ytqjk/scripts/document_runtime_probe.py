"""Offline import and provider probe executed by the isolated runtime."""

from __future__ import annotations

import importlib
import importlib.metadata
import json
import os


_PACKAGES = (
    "docling",
    "rapidocr",
    "onnxruntime",
    "paddleocr",
    "paddlepaddle",
    "transformers",
    "torch",
    "huggingface-hub",
    "pypdfium2",
    "Pillow",
    "numpy",
)
_MODULES = {
    "docling": "docling",
    "rapidocr": "rapidocr",
    "onnxruntime": "onnxruntime",
    "paddleocr": "paddleocr",
    "paddlepaddle": "paddle",
    "transformers": "transformers",
    "torch": "torch",
    "huggingface-hub": "huggingface_hub",
    "pypdfium2": "pypdfium2",
    "Pillow": "PIL",
    "numpy": "numpy",
}
_OFFLINE_ENVIRONMENT = {
    "DOCLING_OFFLINE_MODE": "1",
    "HF_HUB_OFFLINE": "1",
    "PADDLE_PDX_DISABLE_MODEL_SOURCE_CHECK": "True",
    "TRANSFORMERS_OFFLINE": "1",
}
_SENTINEL = "YTQJK_RUNTIME_PROBE="


def main() -> int:
    os.environ.update(_OFFLINE_ENVIRONMENT)
    imported: list[str] = []
    modules: dict[str, object] = {}
    for package, module_name in _MODULES.items():
        try:
            modules[package] = importlib.import_module(module_name)
            imported.append(package)
        except Exception:
            continue
    providers: list[str] = []
    onnx = modules.get("onnxruntime")
    if onnx is not None:
        try:
            value = onnx.get_available_providers()
            if type(value) is list:
                providers = value
        except Exception:
            providers = []
    distributions = sorted({
        item.metadata["Name"]
        for item in importlib.metadata.distributions()
        if item.metadata["Name"]
    })
    payload = {
        "packages": {
            name: importlib.metadata.version(name)
            for name in _PACKAGES
        },
        "distributions": distributions,
        "imports": imported,
        "onnx_providers": providers,
    }
    print(_SENTINEL + json.dumps(payload, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
