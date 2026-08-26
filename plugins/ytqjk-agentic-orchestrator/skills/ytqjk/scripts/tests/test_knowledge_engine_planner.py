from __future__ import annotations

import sys
from pathlib import Path
from types import SimpleNamespace

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
DASHBOARD = SCRIPTS.parent / "dashboard"
sys.path[:0] = [str(SCRIPTS), str(DASHBOARD)]

import knowledge_engine_planner as planner_module  # noqa: E402
from knowledge_engine_models import (  # noqa: E402
    ModelManifestError,
    ModelSettings,
)
from knowledge_engine_planner import (  # noqa: E402
    EngineNotConfigured,
    EnginePlanner,
)


class Engine:
    def __init__(self, modules: dict[str, object]) -> None:
        self.modules = modules

    def module(self, name: str) -> object:
        return self.modules[name]


def _settings(tmp_path: Path) -> ModelSettings:
    return ModelSettings(
        tmp_path,
        {},
        {"rec_keys": "keys", "det": "det", "cls": "cls", "rec": "rec"},
        {},
        tmp_path / "smol",
    )


def test_settings_are_read_once_and_verified_each_time(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    settings = _settings(tmp_path)
    reads: list[Path] = []
    verifies: list[ModelSettings] = []
    monkeypatch.setattr(
        planner_module,
        "read_model_settings",
        lambda root: reads.append(root) or settings,
    )
    monkeypatch.setattr(
        planner_module,
        "verify_model_settings",
        lambda value: verifies.append(value),
    )
    planner = EnginePlanner(tmp_path, Engine({}))
    assert planner._settings() is settings
    assert planner._settings() is settings
    assert reads == [tmp_path]
    assert verifies == [settings, settings]


def test_changed_settings_clear_all_engine_caches(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    settings = _settings(tmp_path)
    monkeypatch.setattr(
        planner_module,
        "read_model_settings",
        lambda root: settings,
    )
    monkeypatch.setattr(
        planner_module,
        "verify_model_settings",
        lambda value: (_ for _ in ()).throw(
            ModelManifestError("changed")
        ),
    )
    planner = EnginePlanner(tmp_path, Engine({}))
    planner._image_extractor = object()
    planner._pdf_extractor = object()
    with pytest.raises(EngineNotConfigured):
        planner._settings()
    assert planner._image_extractor is None
    assert planner._pdf_extractor is None


def test_image_and_pdf_extractors_are_reused(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
) -> None:
    settings = _settings(tmp_path)
    image_builds: list[object] = []
    pdf_builds: list[object] = []

    class ImageExtractor:
        def __init__(self, *args: object, **kwargs: object) -> None:
            image_builds.append((args, kwargs))

        @staticmethod
        def extract(source: bytes, name: str) -> object:
            return SimpleNamespace(
                status=SimpleNamespace(value="SUCCEEDED"),
                result=(source, name),
            )

    class PdfError(RuntimeError):
        code = "BACKEND_FAILURE"

    class PdfExtractor:
        def __init__(self, *args: object, **kwargs: object) -> None:
            pdf_builds.append((args, kwargs))

        @staticmethod
        def extract(source: bytes) -> bytes:
            return source

    class PdfBackend:
        def __init__(self, *args: object, **kwargs: object) -> None:
            pass

    modules = {
        "image_document_extractor": SimpleNamespace(
            ImageDocumentExtractor=ImageExtractor,
        ),
        "docling_backend": SimpleNamespace(DoclingBackend=PdfBackend),
        "pdf_document_extractor": SimpleNamespace(
            PdfDocumentExtractor=PdfExtractor,
            PdfExtractionError=PdfError,
        ),
    }
    planner = EnginePlanner(tmp_path, Engine(modules))
    planner._model_settings = settings
    monkeypatch.setattr(
        planner_module,
        "verify_model_settings",
        lambda value: None,
    )
    monkeypatch.setattr(
        planner_module,
        "build_vision",
        lambda modules, value: (object(), object(), object()),
    )
    monkeypatch.setattr(
        planner_module,
        "build_pdf_secondary",
        lambda modules, value: object(),
    )
    assert planner._image(b"one", "one.png") == (b"one", "one.png")
    assert planner._image(b"two", "two.png") == (b"two", "two.png")
    assert planner._pdf(b"first") == b"first"
    assert planner._pdf(b"second") == b"second"
    assert len(image_builds) == 1
    assert len(pdf_builds) == 1
