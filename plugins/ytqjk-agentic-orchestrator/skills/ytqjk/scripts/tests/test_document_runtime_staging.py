from __future__ import annotations

import sys
from pathlib import Path


TESTS = Path(__file__).resolve().parent
SCRIPTS = TESTS.parent
DASHBOARD = SCRIPTS.parent / "dashboard"
sys.path[:0] = [str(SCRIPTS), str(DASHBOARD)]

from document_runtime import DocumentRuntime  # noqa: E402
from document_runtime_service import (  # noqa: E402
    _prepare_stage,
    _stage_targets,
)


def test_windows_stage_uses_short_contained_paths(tmp_path: Path) -> None:
    manager = DocumentRuntime(
        tmp_path / "knowledge",
        platform_name="win32",
    )
    stage = _prepare_stage(manager)
    venv, models = _stage_targets(manager, stage)

    relative = stage.relative_to(manager.root)
    assert relative.parts[0] == ".di"
    assert len(relative.parts[1]) == 32
    assert (venv.name, models.name) == ("v", "m")

    package_tail = Path(
        "Lib/site-packages/torch-2.13.0+cpu.dist-info/licenses/"
        "third_party/kineto/libkineto/third_party/dynolog/"
        "third_party/DCGM/testing/python3/libs_3rdparty/colorama"
    )
    representative = Path("D:/knowledge") / relative / "v" / package_tail
    assert len(str(representative)) < 220


def test_linux_stage_keeps_descriptive_layout(tmp_path: Path) -> None:
    manager = DocumentRuntime(
        tmp_path / "knowledge",
        platform_name="linux",
    )
    stage = _prepare_stage(manager)
    venv, models = _stage_targets(manager, stage)

    relative = stage.relative_to(manager.runtime)
    assert relative.parts[0] == "install-staging"
    assert (venv.name, models.name) == ("venv", "models")
