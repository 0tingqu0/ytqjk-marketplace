"""Immutable official model-download plan; execution stays in staging."""

from __future__ import annotations

from pathlib import Path

from document_runtime_models import (
    DOCLING_CLASSIFIER,
    DOCLING_LAYOUT,
    DOCLING_TABLE,
    PADDLE_DET,
    PADDLE_LAYOUT,
    PADDLE_REC,
    PADDLE_TABLE_CLASSIFIER,
    PADDLE_WIRED_CELLS,
    PADDLE_WIRED_STRUCTURE,
    PADDLE_WIRELESS_CELLS,
    PADDLE_WIRELESS_STRUCTURE,
    SMOLVLM,
)


_JSON_MODEL = ("*.json", "*.yml", "*.pdiparams")
_DOWNLOADS = (
    (
        "docling-project/docling-layout-heron-onnx",
        "40bde044036bb181c130ddf6c51792187268748f",
        DOCLING_LAYOUT,
        ("*.json", "*.onnx"),
    ),
    (
        "docling-project/docling-models",
        "fc0f2d45e2218ea24bce5045f58a389aed16dc23",
        DOCLING_TABLE,
        ("*.json", "*.safetensors"),
    ),
    (
        "docling-project/DocumentFigureClassifier-v2.5",
        "f859dfbff5c9916cd996942d4b0db7fa25808220",
        DOCLING_CLASSIFIER,
        ("*.json", "*.onnx"),
    ),
    (
        "PaddlePaddle/PP-OCRv6_medium_det",
        "8e0f56fb2ef86b461d99cfc7ac5c137738985f61",
        PADDLE_DET,
        _JSON_MODEL,
    ),
    (
        "PaddlePaddle/PP-OCRv6_medium_rec",
        "e5a92bcbc5cc1b494628e458d267778f0704fd7c",
        PADDLE_REC,
        _JSON_MODEL,
    ),
    (
        "PaddlePaddle/PP-DocLayout_plus-L",
        "aa52b8528c84f9b1a34ac3a88fe0e576edb9d11d",
        PADDLE_LAYOUT,
        _JSON_MODEL,
    ),
    (
        "PaddlePaddle/PP-LCNet_x1_0_table_cls",
        "2fa6323e7dab88fa883081db1460995f46af2922",
        PADDLE_TABLE_CLASSIFIER,
        _JSON_MODEL,
    ),
    (
        "PaddlePaddle/SLANeXt_wired",
        "763069fcda6a065f2171753205a32bf899a88d15",
        PADDLE_WIRED_STRUCTURE,
        _JSON_MODEL,
    ),
    (
        "PaddlePaddle/SLANet_plus",
        "bae6e5f8c3c4e7da0c0b7639fdf3228fe76184e2",
        PADDLE_WIRELESS_STRUCTURE,
        _JSON_MODEL,
    ),
    (
        "PaddlePaddle/RT-DETR-L_wired_table_cell_det",
        "e2bd53c06b3a815d86acbf5c6779dada58819cfe",
        PADDLE_WIRED_CELLS,
        _JSON_MODEL,
    ),
    (
        "PaddlePaddle/RT-DETR-L_wireless_table_cell_det",
        "25ca86356a601c877476bb0dcc5fd09153d9d64d",
        PADDLE_WIRELESS_CELLS,
        _JSON_MODEL,
    ),
    (
        "HuggingFaceTB/SmolVLM-256M-Instruct",
        "7e3e67edbbed1bf9888184d9df282b700a323964",
        SMOLVLM,
        ("*.json", "*.txt", "*.safetensors"),
    ),
)


def download_commands(
    tool: Path,
    output: Path,
    windows: bool,
) -> tuple[tuple[list[str], int], ...]:
    hf = tool.parent / ("hf.exe" if windows else "hf")
    commands = [
        (
            _hf(
                hf,
                repo,
                revision,
                _download_target(output, target, index, windows),
                includes,
            ),
            7200,
        )
        for index, (repo, revision, target, includes)
        in enumerate(_DOWNLOADS)
    ]
    python = tool.parent / ("python.exe" if windows else "python")
    materializer = Path(__file__).with_name("document_runtime_assets.py")
    materialize = [
        str(python),
        str(materializer),
        "--output",
        str(output),
    ]
    if windows:
        materialize.append("--windows-layout")
    commands.append((materialize, 600))
    return tuple(commands)


def windows_download_layout() -> tuple[tuple[str, str], ...]:
    return tuple(
        (f".w/{index:02d}", target)
        for index, (_, _, target, _) in enumerate(_DOWNLOADS)
    )


def _download_target(
    output: Path,
    target: str,
    index: int,
    windows: bool,
) -> Path:
    relative = f".w/{index:02d}" if windows else target
    return output / relative


def _hf(
    tool: Path,
    repo: str,
    revision: str,
    output: Path,
    includes: tuple[str, ...],
) -> list[str]:
    command = [
        str(tool),
        "download",
        repo,
        "--revision",
        revision,
        "--local-dir",
        str(output),
    ]
    for pattern in includes:
        command.extend(("--include", pattern))
    return command


__all__ = ["download_commands", "windows_download_layout"]
