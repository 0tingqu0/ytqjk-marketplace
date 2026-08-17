"""Shared installer receipt helpers."""
from __future__ import annotations

import json
import platform
import shutil
from collections.abc import Mapping
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


def summary_text(value: object) -> str:
    """Return a concise human-readable installer result."""
    if not isinstance(value, Mapping):
        return "YTQJK：安装回执无效。"
    version = str(value.get("version") or "unknown")
    operation = str(value.get("operation") or "install")
    dry_run = value.get("dry_run") is True
    apply = _record(value.get("apply"))
    knowledge = _record(value.get("knowledge_bootstrap"))
    imported = _record(value.get("knowledge_import"))
    guidance = _record(value.get("codex_guidance"))
    dashboard = _record(value.get("dashboard_service"))
    statuses = (
        _status(knowledge), _status(imported),
        _status(guidance), _status(dashboard),
    )
    apply_status = _status(apply)
    expected_apply = "UNINSTALLED" if operation == "uninstall" else "APPLIED"
    failed = "FAILED" in statuses or (
        not dry_run and apply_status != expected_apply
    )
    warned = _status(imported) == "SUCCEEDED_WITH_WARNINGS"
    if dry_run:
        headline = "卸载预览" if operation == "uninstall" else "安装预览"
    elif failed:
        headline = "卸载未完全成功" if operation == "uninstall" else "安装未完全成功"
    elif warned:
        headline = "安装完成（有警告）"
    else:
        headline = "卸载完成" if operation == "uninstall" else "安装完成"
    lines = [f"YTQJK v{version}：{headline}"]
    if dry_run:
        actions = value.get("actions")
        action_count = len(actions) if isinstance(actions, list) else 0
        lines.append(f"- 计划操作：{action_count} 项（尚未写入）")
        return "\n".join(lines)
    lines.append(
        "- 插件与技能：已卸载"
        if apply_status == "UNINSTALLED"
        else "- 插件与技能：已安装或更新"
        if apply_status == "APPLIED"
        else "- 插件与技能：失败"
    )
    _append_knowledge(lines, knowledge)
    _append_import(lines, imported)
    _append_guidance(lines, guidance)
    _append_dashboard(lines, dashboard)
    if operation == "uninstall":
        lines.append("- 知识库数据：保留")
    if failed or warned:
        lines.append("详情：重新运行安装命令并追加 --json")
    return "\n".join(lines)


def _record(value: object) -> Mapping[str, object]:
    return value if isinstance(value, Mapping) else {}


def _status(value: Mapping[str, object]) -> str:
    return str(value.get("status") or "UNKNOWN")


def _count(value: Mapping[str, object], key: str) -> int:
    count = value.get(key)
    return count if isinstance(count, int) and not isinstance(count, bool) else 0


def _append_knowledge(
    lines: list[str], knowledge: Mapping[str, object]
) -> None:
    status = _status(knowledge)
    if status == "SUCCEEDED":
        lines.append(
            "- 知识库：就绪"
            f"（总库 {_count(knowledge, 'global_files')} 个文件，"
            f"项目 {_count(knowledge, 'project_files')} 个文件）"
        )
    elif status == "FAILED":
        lines.append("- 知识库：初始化失败")
    elif status == "NOT_CONFIGURED":
        lines.append("- 知识库：未配置项目索引")


def _append_import(
    lines: list[str], imported: Mapping[str, object]
) -> None:
    status = _status(imported)
    if status in ("SUCCEEDED", "SUCCEEDED_WITH_WARNINGS"):
        text = f"- 资料导入：{_count(imported, 'imported_count')} 个成功"
        parse_failed = _count(imported, "parse_failed_count")
        if parse_failed:
            text += f"，{parse_failed} 个未解析"
        lines.append(text)
    elif status == "FAILED":
        lines.append("- 资料导入：失败")


def _append_guidance(
    lines: list[str], guidance: Mapping[str, object]
) -> None:
    status = _status(guidance)
    if status == "INSTALLED":
        target = str(guidance.get("target") or "AGENTS.md")
        lines.append(f"- 新会话接入：已配置（{target}）")
    elif status == "REMOVED":
        lines.append("- 新会话接入：已移除")
    elif status == "FAILED":
        lines.append("- 新会话接入：配置失败")


def _append_dashboard(
    lines: list[str], dashboard: Mapping[str, object]
) -> None:
    status = _status(dashboard)
    port = _count(dashboard, "port") or 8765
    if status == "RUNNING":
        lines.append(f"- 后台网页：运行中（http://127.0.0.1:{port}）")
    elif status == "STOPPED":
        lines.append("- 后台网页：已停止")
    elif status == "FAILED":
        lines.append(f"- 后台网页：启动失败（端口 {port}）")
