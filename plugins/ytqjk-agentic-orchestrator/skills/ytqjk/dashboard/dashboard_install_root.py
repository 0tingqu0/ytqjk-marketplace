"""Resolve the Codex root owned by a dashboard update request."""
from __future__ import annotations

from pathlib import Path


PLUGIN_NAME = "ytqjk-agentic-orchestrator"
MANAGED_NAME = ".ytqjk-managed.json"


class InstallRootError(ValueError):
    """Raised when the running dashboard cannot own plugin updates."""


def managed_codex_root(plugin_root: Path, version: str) -> Path:
    root = plugin_root.resolve()
    if root.name == PLUGIN_NAME and root.parent.name == "plugins":
        managed = root.parent / MANAGED_NAME
        if not managed.is_file() or managed.is_symlink():
            raise InstallRootError("当前插件不受 YTQJK 安装器管理。")
        return root.parent.parent
    if _is_dashboard_bundle(root):
        if root.name != version:
            raise InstallRootError("当前控制台快照版本无效。")
        return root.parents[3]
    raise InstallRootError("当前插件不是稳定安装目录或控制台快照。")


def _is_dashboard_bundle(root: Path) -> bool:
    return (
        len(root.parents) >= 4
        and root.parent.name == "dashboard-service"
        and root.parents[1].name == "ytqjk"
        and root.parents[2].name == "data"
    )


__all__ = ["InstallRootError", "managed_codex_root"]
