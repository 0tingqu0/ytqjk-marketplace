"""Select Docling's pinned local ONNX layout engine."""

from __future__ import annotations

from types import ModuleType


LAYOUT_PRESET = "layout_heron_default"


def configure_layout(
    options: ModuleType,
    engines: ModuleType,
    keywords: dict[str, object],
) -> None:
    engine = engines.OnnxRuntimeObjectDetectionEngineOptions()
    keywords["layout_options"] = (
        options.LayoutObjectDetectionOptions.from_preset(
            LAYOUT_PRESET,
            engine_options=engine,
        )
    )


__all__ = ["LAYOUT_PRESET", "configure_layout"]
