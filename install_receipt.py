"""Shared installer receipt helpers."""
from __future__ import annotations

import json
import platform
import shutil
from pathlib import Path

from install_core import VERSION, Plan, target_has_grill_me
from uninstall_core import UninstallPlan


VECTOR_LIMIT_BYTES = 10 * 1024 * 1024
VECTOR_LIMIT_CHUNKS = 2000


def vector_result(
    mode: str, size: int, chunks: int, failed: bool = False
) -> str:
    if failed:
        return "lexical-only"
    if mode in ("off", "on"):
        return "off" if mode == "off" else "planned"
    large = size >= VECTOR_LIMIT_BYTES or chunks >= VECTOR_LIMIT_CHUNKS
    return "planned" if large else "lexical-only"


def health(
    probe: bool, executable_overrides: dict[str, str] | None = None
) -> dict[str, str]:
    names = ("python", "node", "npm", "codex")
    overrides = executable_overrides or {}
    checks = {
        name: (
            "AVAILABLE"
            if probe and (name in overrides or shutil.which(name))
            else "MISSING" if probe else "UNKNOWN"
        )
        for name in names
    }
    unknown = (
        "core_task_api", "plugin_discovery", "skill_discovery",
        "knowledge_service", "loopback_workbench", "vector",
    )
    checks.update({name: "UNKNOWN" for name in unknown})
    return checks


def receipt(
    plan: Plan | UninstallPlan,
    target: Path | None,
    applied: bool,
    health_info: dict[str, str],
    vector: str,
    operation: str = "install",
) -> dict[str, object]:
    return {
        "schema": "ytqjk-install-receipt/v1",
        "version": VERSION,
        "operation": operation,
        "mode": plan.mode,
        "dry_run": not applied,
        "target_root": "CONFIGURED" if target else "NOT_CONFIGURED",
        "actions": list(plan.actions),
        "copies": [source.name for source, _ in plan.files],
        "removals": [path.name for path in getattr(plan, "paths", ())],
        "grill_me_present": target_has_grill_me(target) if target else False,
        "health": health_info,
        "vector": vector,
        "platform": platform.system(),
        "sqlite_note": (
            "SQLite caches are not shared across Windows, Linux, WSL."
        ),
    }


def json_text(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True)
