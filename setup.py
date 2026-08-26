"""YTQJK installer entry point."""
from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path
from typing import Callable

from bootstrap_cli_runtime import CliRuntime, ensure_cli_runtime
from codex_bootstrap_import import (
    default_codex_root,
    default_knowledge_root,
    empty_receipt as empty_import_receipt,
    failed_receipt,
    import_codex_candidates,
)
from codex_plugin_paths import prepare_codex_root
from codex_guidance import configure as configure_guidance
from codex_guidance import receipt as guidance_receipt
from dashboard_service_install import (
    apply_dashboard_configuration, configure_dashboard, dashboard_receipt,
    schedule_dashboard_restart,
)
from external_command_runner import run_external
from install_core import (
    MODES, PUBLIC_MODES, VERSION, InstallError, apply_plan, build_plan,
    normalize_update_mode, require_python, target_has_grill_me,
)
from install_receipt import (
    health, json_text, receipt, summary_text, vector_result,
)
from project_bootstrap import bootstrap_project, bootstrap_receipt
from uninstall_core import apply_uninstall_plan, build_uninstall_plan


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
    result.add_argument(
        "--dashboard-service", choices=("auto", "off"), default="auto",
        help=argparse.SUPPRESS,
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
        if not args.uninstall:
            args.mode = normalize_update_mode(
                args.mode, args.target_root, args.codex_import,
                args.project_bootstrap, args.dashboard_service,
            )
        plan = (
            build_uninstall_plan(args.mode, args.target_root)
            if args.uninstall else build_plan(args.mode, args.target_root)
        )
        applied = bool(args.apply)
        cli_runtime = CliRuntime("NOT_REQUIRED", None, {}, None)
        effective_runner = runner
        codex_root = default_codex_root(args.codex_root)
        if applied and runner is None:
            required = {
                "codex" if action.get("kind") == "codex" else "npx"
                for action in plan.actions
                if action.get("kind") in ("codex", "third-party-stage")
            }
            external_environment = os.environ.copy()
            if "codex" in required or args.mode == "codex-stable-only":
                codex_root = prepare_codex_root(codex_root)
                external_environment["CODEX_HOME"] = str(codex_root)
            cli_runtime = ensure_cli_runtime(required)
            if cli_runtime.environment:
                external_environment.update(cli_runtime.environment)
                external_environment["CODEX_HOME"] = str(codex_root)

            def production_runner(command: list[str], cwd: Path) -> str:
                return run_external(
                    command,
                    cwd,
                    dict(cli_runtime.executables),
                    external_environment,
                )

            effective_runner = production_runner
        result = receipt(
            plan, args.target_root, applied,
            health(args.probe_local, dict(cli_runtime.executables)),
            vector_result(
                args.vector, args.knowledge_bytes, args.knowledge_chunks,
                args.vector_failed,
            ),
            "uninstall" if args.uninstall else "install",
        )
        result["cli_runtime"] = cli_runtime.receipt()
        result["knowledge_import"] = empty_import_receipt(
            "SKIPPED_UNINSTALL" if args.uninstall else "SKIPPED_DRY_RUN"
        )
        result["knowledge_bootstrap"] = bootstrap_receipt(
            "SKIPPED_UNINSTALL" if args.uninstall else "SKIPPED_DRY_RUN"
        )
        result["dashboard_service"] = dashboard_receipt(
            "SKIPPED_UNINSTALL" if args.uninstall else "SKIPPED_DRY_RUN"
        )
        result["codex_guidance"] = guidance_receipt(
            "SKIPPED_UNINSTALL" if args.uninstall else "SKIPPED_DRY_RUN"
        )
        if applied:
            if args.uninstall:
                result["dashboard_service"] = apply_dashboard_configuration(
                    configure_dashboard, codex_root,
                    default_knowledge_root(args.knowledge_root),
                    args.mode, "uninstall",
                )
                result["apply"] = apply_uninstall_plan(
                    plan, args.target_root,
                    runner=effective_runner or run_external,
                    codex_root=codex_root,
                )
                result["codex_guidance"] = configure_guidance(
                    codex_root, default_knowledge_root(args.knowledge_root),
                    args.mode, "uninstall",
                )
            else:
                result["apply"] = apply_plan(
                    plan, args.target_root, args.fail_after_copy,
                    runner=effective_runner or run_external,
                    codex_root=codex_root,
                )
                result["codex_guidance"] = configure_guidance(
                    codex_root, default_knowledge_root(args.knowledge_root),
                    args.mode, "install",
                )
            result["grill_me_present"] = target_has_grill_me(
                args.target_root
            )
            if args.uninstall:
                output = json_text(result) if args.json else summary_text(result)
                print(output)
                if result["codex_guidance"]["status"] == "FAILED":
                    return 6
                dashboard_failed = (
                    result["dashboard_service"]["status"] == "FAILED"
                )
                return 5 if dashboard_failed else 0
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
            legacy_update = (
                args.dashboard_service == "auto"
                and args.mode == "codex-only"
                and args.codex_import == "off"
                and args.project_bootstrap == "off"
                and args.target_root is not None
                and any(
                    part.name.startswith("ytqjk-update-")
                    for part in (
                        args.target_root.resolve(),
                        *args.target_root.resolve().parents,
                    )
                )
            )
            deferred_update = args.mode == "codex-stable-only" or legacy_update
            if args.dashboard_service == "off":
                result["dashboard_service"] = dashboard_receipt(
                    "SKIPPED_UPDATE"
                )
            elif deferred_update:
                try:
                    result["dashboard_service"] = schedule_dashboard_restart(
                        codex_root,
                        default_knowledge_root(args.knowledge_root),
                    )
                except Exception:
                    result["dashboard_service"] = dashboard_receipt("FAILED")
            else:
                result["dashboard_service"] = apply_dashboard_configuration(
                    configure_dashboard, codex_root,
                    default_knowledge_root(args.knowledge_root),
                    args.mode, "install",
                )
        output = json_text(result) if args.json else summary_text(result)
        print(output)
        import_failed = (
            result["knowledge_import"]["status"] == "FAILED"
        )
        bootstrap_failed = (
            result["knowledge_bootstrap"]["status"] == "FAILED"
        )
        dashboard_failed = result["dashboard_service"]["status"] == "FAILED"
        guidance_failed = result["codex_guidance"]["status"] == "FAILED"
        if import_failed:
            return 3
        if bootstrap_failed:
            return 4
        if guidance_failed:
            return 6
        return 5 if dashboard_failed else 0
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
