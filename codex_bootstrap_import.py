"""Safe first-install import of selected Codex files as candidates."""
from __future__ import annotations

import hashlib
import importlib.util
import os
import sys
import types
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from codex_import_receipt import (
    apply_previous_result,
    apply_service_result,
    fail,
    new_receipt,
)
from codex_import_safety import (
    SourceBlockedError,
    excluded_name,
    paths_overlap,
    reject_link as _reject_link,
    safe_absolute as _safe_absolute,
)

IMPORT_SCHEMA = "codex-bootstrap-v1"
SUPPORTED_SUFFIXES = frozenset(
    {".csv", ".json", ".markdown", ".md", ".tsv", ".txt"}
)
NOT_CONFIGURED_SUFFIXES = frozenset(
    {
        ".aac", ".bmp", ".doc", ".docx", ".flac", ".gif", ".jpeg",
        ".jpg", ".m4a", ".mp3", ".ogg", ".pdf", ".png", ".ppt",
        ".pptx", ".tif", ".tiff", ".wav", ".webp", ".xls", ".xlsx",
    }
)


@dataclass(frozen=True)
class Dependencies:
    """Lazy adapter boundary for the packaged knowledge service."""

    inspect_input: Callable[..., Sequence[Any]]
    registry: Callable[..., Any]
    service: Callable[[Path], Any]
    import_candidate: Callable[..., Any]


def empty_receipt(
    status: str, marker_sha256: str | None = None
) -> dict[str, object]:
    """Return a path- and content-free import receipt."""
    return new_receipt(status, marker_sha256)


def failed_receipt(stage: str, code: str) -> dict[str, object]:
    """Return one stable sanitized adapter failure receipt."""
    return fail(empty_receipt("FAILED"), stage, code)


def default_codex_root(
    explicit: Path | None, environ: Mapping[str, str] | None = None
) -> Path:
    """Resolve Codex home without consulting installer target-root."""
    if explicit is not None:
        return explicit.expanduser()
    values = os.environ if environ is None else environ
    configured = values.get("CODEX_HOME", "").strip()
    if configured:
        return Path(configured).expanduser()
    return Path.home() / ".codex"


def default_knowledge_root(explicit: Path | None) -> Path:
    """Reuse the packaged cross-platform knowledge-root policy."""
    if explicit is not None:
        return explicit.expanduser()
    root = Path(__file__).resolve().parent
    source = (
        root / "plugins" / "ytqjk-agentic-orchestrator" / "skills"
        / "ytqjk" / "scripts" / "platform_paths.py"
    )
    module = _load_module("_ytqjk_installer_platform_paths", source)
    return module.default_knowledge_root()


def import_codex_candidates(
    codex_root: Path,
    knowledge_root: Path,
    mode: str,
    *,
    dependencies: Dependencies | None = None,
) -> dict[str, object]:
    """Import the fixed Codex allowlist through the public service API."""
    receipt = empty_receipt("FAILED")
    stage = "SOURCE_ROOT"
    try:
        source_root = _safe_absolute(codex_root, must_exist=False)
        if not source_root.exists():
            return empty_receipt("SKIPPED_NO_SOURCE")
        if not source_root.is_dir():
            raise SourceBlockedError("Codex source root must be a directory")
        stage = "TARGET_ROOT"
        target_root = _safe_absolute(knowledge_root, must_exist=False)
        if paths_overlap(source_root, target_root):
            raise SourceBlockedError("source and target roots overlap")
        stage = "ADAPTER_LOAD"
        active = dependencies or _load_dependencies()
        database = target_root / "service" / "knowledge.sqlite3"
        origin = _digest(os.path.normcase(str(source_root)))
        marker = f"{IMPORT_SCHEMA}-{origin}"
        receipt["marker_sha256"] = _digest(marker)
        stage = "MARKER_READ"
        service = active.service(database)
        prior = service.import_receipt(marker)
        if mode == "auto" and prior is not None:
            skipped = empty_receipt("SKIPPED_MARKER", _digest(marker))
            apply_previous_result(skipped, prior)
            return skipped
        receipt["marker_status"] = "MISS"
        stage = "DISCOVERY"
        files, excluded, not_configured = _discover(source_root)
        receipt["discovered_count"] = len(files)
        receipt["excluded_count"] = excluded
        receipt["not_configured_count"] = not_configured
        registry = active.registry(allow_extensionless_text=True)
        candidates = []
        manifest_entries = []
        for relative, path in files:
            stage = "INSPECTION"
            try:
                inspected = active.inspect_input(source_root, path)
            except Exception:
                receipt["blocked_count"] = (
                    int(receipt["blocked_count"]) + 1
                )
                raise
            if len(inspected) != 1:
                raise RuntimeError("single-file inspection contract violated")
            receipt["scanner"] = "local-pattern-v1"
            stage = "PARSING"
            try:
                parsed = registry.parse(inspected[0])
            except Exception:
                receipt["parse_failed_count"] = (
                    int(receipt["parse_failed_count"]) + 1
                )
                if mode == "auto":
                    continue
                raise
            candidates.append(active.import_candidate(
                title=relative,
                parsed=parsed,
                source_kind=f"codex-bootstrap-{origin[:12]}",
                governance_state="CANDIDATE",
            ))
            manifest_entries.append(f"{relative}\0{inspected[0].sha256}")
        manifest = _digest("\n".join(manifest_entries))
        receipt["manifest_sha256"] = manifest
        stage = "CANDIDATE_WRITE"
        service_receipt = service.import_candidates(
            "global", "codex-bootstrap", marker, tuple(candidates),
            force=mode == "force",
        )
        receipt["service_receipt_sha256"] = (
            service_receipt.receipt_sha256
        )
        if service_receipt.status == "SKIPPED":
            apply_previous_result(receipt, service_receipt)
        else:
            apply_service_result(
                receipt, service_receipt, replay=mode == "force"
            )
        if receipt["parse_failed_count"]:
            receipt.update({
                "status": "SUCCEEDED_WITH_WARNINGS",
                "failure_stage": "PARSING",
                "failure_code": "PARSE_FAILED",
            })
        return receipt
    except SourceBlockedError:
        receipt["blocked_count"] = int(receipt["blocked_count"]) + 1
        return fail(receipt, stage, "SOURCE_BLOCKED")
    except Exception:
        codes = {
            "INSPECTION": "INSPECTION_FAILED",
            "PARSING": "PARSE_FAILED",
            "SOURCE_ROOT": "SOURCE_ROOT_INVALID",
            "TARGET_ROOT": "TARGET_ROOT_INVALID",
            "ADAPTER_LOAD": "ADAPTER_UNAVAILABLE",
            "MARKER_READ": "MARKER_READ_FAILED",
            "DISCOVERY": "DISCOVERY_FAILED",
            "CANDIDATE_WRITE": "CANDIDATE_WRITE_FAILED",
        }
        return fail(receipt, stage, codes.get(stage, "IMPORT_FAILED"))


def _discover(
    root: Path,
) -> tuple[list[tuple[str, Path]], int, int]:
    files: list[tuple[str, Path]] = []
    excluded = 0
    not_configured = 0
    memory = root / "mem.md"
    if memory.exists() or memory.is_symlink():
        _reject_link(memory)
        if not memory.is_file():
            raise SourceBlockedError(
                "allowlisted memory is not a regular file"
            )
        files.append(("mem.md", memory))
    for name in ("memories", "knowledge", "attachments"):
        directory = root / name
        if not directory.exists() and not directory.is_symlink():
            continue
        _reject_link(directory)
        if not directory.is_dir():
            raise SourceBlockedError(
                "allowlisted source is not a directory"
            )
        found, skipped, unavailable = _walk(root, directory)
        files.extend(found)
        excluded += skipped
        not_configured += unavailable
    files.sort(key=lambda item: item[0])
    return files, excluded, not_configured


def _walk(
    root: Path, directory: Path
) -> tuple[list[tuple[str, Path]], int, int]:
    files: list[tuple[str, Path]] = []
    excluded = 0
    not_configured = 0
    with os.scandir(directory) as entries:
        ordered = sorted(entries, key=lambda item: item.name.casefold())
    for entry in ordered:
        path = Path(entry.path)
        _reject_link(path)
        if excluded_name(entry.name):
            excluded += 1
            continue
        if entry.is_dir(follow_symlinks=False):
            found, skipped, unavailable = _walk(root, path)
            files.extend(found)
            excluded += skipped
            not_configured += unavailable
        elif entry.is_file(follow_symlinks=False):
            if path.suffix.casefold() in NOT_CONFIGURED_SUFFIXES:
                not_configured += 1
                continue
            if not _supported_file(root, path):
                excluded += 1
                continue
            relative = path.relative_to(root).as_posix()
            files.append((relative, path))
        else:
            raise SourceBlockedError("non-regular allowlisted input")
    return files, excluded, not_configured


def _supported_file(root: Path, path: Path) -> bool:
    suffix = path.suffix.casefold()
    if suffix in SUPPORTED_SUFFIXES:
        return True
    relative = path.relative_to(root)
    return (
        not suffix
        and not path.name.startswith(".")
        and relative.parts[0].casefold() == "memories"
    )


def _load_dependencies() -> Dependencies:
    root = Path(__file__).resolve().parent
    scripts = (
        root / "plugins" / "ytqjk-knowledge" / "skills"
        / "ytqjk-knowledge" / "scripts"
    )
    package = "_ytqjk_installer_knowledge"
    if package not in sys.modules:
        module = types.ModuleType(package)
        module.__path__ = [str(scripts)]
        sys.modules[package] = module
    security = _load_module(
        f"{package}.intake_security", scripts / "intake_security.py"
    )
    parsers = _load_module(
        f"{package}.intake_parsers", scripts / "intake_parsers.py"
    )
    contracts = _load_module(
        f"{package}.import_contracts", scripts / "import_contracts.py"
    )
    service = _load_module(f"{package}.service", scripts / "service.py")
    return Dependencies(
        security.inspect_input,
        parsers.default_registry,
        service.KnowledgeService,
        contracts.CandidateImport,
    )


def _load_module(name: str, path: Path) -> Any:
    existing = sys.modules.get(name)
    if existing is not None:
        return existing
    spec = importlib.util.spec_from_file_location(name, path)
    if spec is None or spec.loader is None:
        raise RuntimeError("packaged knowledge adapter is unavailable")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    try:
        spec.loader.exec_module(module)
    except Exception:
        sys.modules.pop(name, None)
        raise
    return module


def _digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()
