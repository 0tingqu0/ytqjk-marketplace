"""YTQJK installer entry point."""
from __future__ import annotations

import argparse
import json
import os
import platform
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Callable

from codex_bootstrap_import import (
    default_codex_root,
    default_knowledge_root,
    empty_receipt as empty_import_receipt,
    failed_receipt,
    import_codex_candidates,
)
from install_core import (
    MODES, PUBLIC_MODES, VERSION, InstallError, Plan, apply_plan, build_plan,
    require_python, target_has_grill_me,
)
from project_bootstrap import bootstrap_project, bootstrap_receipt
from uninstall_core import (
    UninstallPlan,
    apply_uninstall_plan,
    build_uninstall_plan,
)

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


def health(probe: bool) -> dict[str, str]:
    names = ("python", "node", "npm", "codex")
    checks = {
        name: ("AVAILABLE" if probe and shutil.which(name) else
               "MISSING" if probe else "UNKNOWN")
        for name in names
    }
    unknown = ("core_task_api", "plugin_discovery", "skill_discovery",
               "knowledge_service", "loopback_workbench", "vector")
    checks.update({name: "UNKNOWN" for name in unknown})
    return checks


def receipt(
    plan: Plan | UninstallPlan, target: Path | None, applied: bool,
    health_info: dict[str, str], vector: str, operation: str = "install",
) -> dict[str, object]:
    return {
        "schema": "ytqjk-install-receipt/v1", "version": VERSION,
        "operation": operation, "mode": plan.mode, "dry_run": not applied,
        "target_root": "CONFIGURED" if target else "NOT_CONFIGURED",
        "actions": list(plan.actions),
        "copies": [source.name for source, _ in plan.files],
        "removals": [path.name for path in getattr(plan, "paths", ())],
        "grill_me_present": target_has_grill_me(target) if target else False,
        "health": health_info, "vector": vector,
        "platform": platform.system(),
        "sqlite_note": (
            "SQLite caches are not shared across Windows, Linux, WSL."
        ),
    }


def json_text(value: object) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser()
    result.add_argument(
        "--mode", choices=MODES, default="all",
        metavar="{" + ",".join(PUBLIC_MODES) + "}",
    )
    result.add_argument("--version", action="version", version=VERSION)
    result.add_argument("--target-root", type=Path)
    result.add_argument("--project-root", type=Path)
    result.add_argument("--codex-root", type=Path)
    result.add_argument("--knowledge-root", type=Path)
    result.add_argument(
        "--codex-import", choices=("auto", "off", "force"),
        default="auto",
    )
    result.add_argument(
        "--project-bootstrap", choices=("auto", "off"), default="auto",
    )
    result.add_argument("--apply", action="store_true")
    result.add_argument("--yes", action="store_true")
    result.add_argument("--uninstall", action="store_true")
    result.add_argument("--json", action="store_true")
    result.add_argument("--health", action="store_true")
    result.add_argument("--probe-local", action="store_true")
    result.add_argument(
        "--vector", choices=("auto", "on", "off"), default="auto"
    )
    result.add_argument("--knowledge-bytes", type=int, default=0)
    result.add_argument("--knowledge-chunks", type=int, default=0)
    result.add_argument("--vector-failed", action="store_true")
    result.add_argument(
        "--fail-after-copy", action="store_true", help=argparse.SUPPRESS
    )
    return result


def run_external(command: list[str], cwd: Path) -> str:
    allowed = {"codex", "npx"}
    if not command or command[0] not in allowed:
        raise RuntimeError("installer rejected external command")
    environment = None
    if command[0] == "npx":
        runtime = cwd / ".ytqjk-npm-runtime"
        home = runtime / "home"
        home.mkdir(parents=True, exist_ok=True)
        environment = os.environ.copy()
        environment.update({
            "HOME": str(home),
            "USERPROFILE": str(home),
            "XDG_CACHE_HOME": str(runtime / "cache"),
            "XDG_CONFIG_HOME": str(runtime / "config"),
            "npm_config_cache": str(runtime / "npm-cache"),
            "npm_config_prefix": str(runtime / "prefix"),
            "npm_config_userconfig": str(runtime / "npmrc"),
        })
    state_check = command[-2:] == ["list", "--json"]
    options: dict[str, object] = {
        "check": True,
        "text": True,
        "shell": False,
        "cwd": cwd,
        "env": environment,
    }
    if state_check:
        options["capture_output"] = True
    else:
        options["stdout"] = sys.stderr
        options["stderr"] = sys.stderr
    try:
        completed = subprocess.run(command, **options)
        return completed.stdout or ""
    except subprocess.CalledProcessError as error:
        detail = (error.stderr or error.stdout or "").strip()
        suffix = f": {detail}" if detail else ""
        raise RuntimeError(
            f"external command failed ({error.returncode}){suffix}"
        ) from error


def main(
    argv: list[str] | None = None,
    runner: Callable[[list[str], Path], str] | None = None,
    codex_importer: Callable[
        [Path, Path, str], dict[str, object]
    ] | None = None,
    project_bootstrapper: Callable[
        [Path, Path, str], dict[str, object]
    ] | None = None,
) -> int:
    args = parser().parse_args(argv)
    try:
        require_python()
        if args.apply and (not args.yes or args.target_root is None):
            raise ValueError(
                "non-interactive apply requires --apply --yes --target-root"
            )
        plan = (
            build_uninstall_plan(args.mode, args.target_root)
            if args.uninstall else build_plan(args.mode, args.target_root)
        )
        applied = bool(args.apply)
        result = receipt(
            plan, args.target_root, applied, health(args.probe_local),
            vector_result(
                args.vector, args.knowledge_bytes, args.knowledge_chunks,
                args.vector_failed,
            ),
            "uninstall" if args.uninstall else "install",
        )
        result["knowledge_import"] = empty_import_receipt(
            "SKIPPED_UNINSTALL" if args.uninstall else "SKIPPED_DRY_RUN"
        )
        result["knowledge_bootstrap"] = bootstrap_receipt(
            "SKIPPED_UNINSTALL" if args.uninstall else "SKIPPED_DRY_RUN"
        )
        if applied:
            codex_root = default_codex_root(args.codex_root)
            if args.uninstall:
                result["apply"] = apply_uninstall_plan(
                    plan, args.target_root, runner=runner or run_external,
                    codex_root=codex_root,
                )
            else:
                result["apply"] = apply_plan(
                    plan, args.target_root, args.fail_after_copy,
                    runner=runner or run_external, codex_root=codex_root,
                )
            result["grill_me_present"] = target_has_grill_me(
                args.target_root
            )
            if args.uninstall:
                output = json_text(result) if args.json else json.dumps(
                    result, indent=2, ensure_ascii=False
                )
                print(output)
                return 0
            eligible = args.mode in ("all", "knowledge-only")
            if not eligible:
                result["knowledge_import"] = empty_import_receipt(
                    "SKIPPED_MODE"
                )
            elif args.codex_import == "off":
                result["knowledge_import"] = empty_import_receipt(
                    "SKIPPED_OFF"
                )
            else:
                importer = codex_importer or import_codex_candidates
                try:
                    result["knowledge_import"] = importer(
                        codex_root,
                        default_knowledge_root(args.knowledge_root),
                        args.codex_import,
                    )
                except Exception:
                    result["knowledge_import"] = failed_receipt(
                        "IMPORTER_CALL", "IMPORTER_FAILED"
                    )
            if not eligible:
                result["knowledge_bootstrap"] = bootstrap_receipt(
                    "SKIPPED_MODE"
                )
            elif args.project_bootstrap == "off":
                result["knowledge_bootstrap"] = bootstrap_receipt(
                    "SKIPPED_OFF"
                )
            elif args.project_root is None:
                result["knowledge_bootstrap"] = bootstrap_receipt(
                    "NOT_CONFIGURED"
                )
            else:
                bootstrapper = project_bootstrapper or bootstrap_project
                try:
                    result["knowledge_bootstrap"] = bootstrapper(
                        default_knowledge_root(args.knowledge_root),
                        args.project_root, args.vector,
                    )
                except Exception:
                    result["knowledge_bootstrap"] = bootstrap_receipt(
                        "FAILED"
                    )
                    result["knowledge_bootstrap"].update({
                        "failure_stage": "BOOTSTRAPPER_CALL",
                        "failure_code": "BOOTSTRAPPER_FAILED",
                    })
        output = json_text(result) if args.json else json.dumps(
            result, indent=2, ensure_ascii=False
        )
        print(output)
        import_failed = (
            result["knowledge_import"]["status"] == "FAILED"
        )
        bootstrap_failed = (
            result["knowledge_bootstrap"]["status"] == "FAILED"
        )
        if import_failed:
            return 3
        return 4 if bootstrap_failed else 0
    except (ValueError, RuntimeError, OSError) as error:
        message = {
            "schema": "ytqjk-install-receipt/v1",
            "version": VERSION,
            "error": str(error),
        }
        if isinstance(error, InstallError):
            message["rollback"] = error.rollback
            message["failed_action"] = error.failed_action
            message["failed_compensations"] = list(
                error.failed_compensations
            )
            message["cleanup"] = error.cleanup
            message["staging_residue"] = error.staging_residue
            message["cleanup_action"] = error.cleanup_action
        print(json_text(message) if args.json else str(error), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
