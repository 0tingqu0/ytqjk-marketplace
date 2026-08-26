from __future__ import annotations

from pathlib import Path

from codex_plugin_paths import source_plugins, tree_hash
from dashboard_bundle import ASSETS, materialize_dashboard_bundle


ROOT = Path(__file__).resolve().parents[1]


def test_dashboard_bundle_is_complete_versioned_and_idempotent(
    tmp_path: Path,
) -> None:
    codex = tmp_path / "codex"
    first = materialize_dashboard_bundle(codex)
    second = materialize_dashboard_bundle(codex)
    source = next(
        item for item in source_plugins(ROOT / "plugins")
        if item.name == "ytqjk-agentic-orchestrator"
    )

    assert first == second
    assert first == codex / "data/ytqjk/dashboard-service" / source.version
    assert tree_hash(first) == tree_hash(source.path)
    for relative in ASSETS:
        assert (first / relative).is_file()


def test_dashboard_bundle_does_not_depend_on_plugin_stable_path(
    tmp_path: Path,
) -> None:
    codex = tmp_path / "codex"
    bundle = materialize_dashboard_bundle(codex)
    plugin_root = codex / "plugins"
    assert not plugin_root.exists()
    assert bundle.is_dir()
