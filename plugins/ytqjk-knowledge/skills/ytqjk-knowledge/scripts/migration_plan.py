"""Read-only legacy migration plans. No source writes or SQLite access."""

from __future__ import annotations

import hashlib
import json
import stat
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any

from .intake_security import IntakeSecurityError, inspect_input


PARTITIONS = {
    "verified": "verified",
    "personal-experience/approved": "approved",
    "error-experience/approved": "approved",
    "personal-experience/candidates": "candidate",
    "error-experience/candidates": "candidate",
}
METADATA_ROOTS = frozenset({"sessions", "projects", "global-cache", "models"})


class MigrationSafetyError(ValueError):
    """Raised when migration evidence or storage safety cannot be proven."""


@dataclass(frozen=True)
class ManifestEntry:
    """Secret-free evidence for source file and required governance state."""

    relative_path: str
    partition: str
    state: str
    size_bytes: int
    modified_ns: int
    sha256: str
    project_alias: str
    downgrade_reason: str | None = None


@dataclass(frozen=True)
class MigrationPlan:
    """Immutable migration inputs; file content is intentionally excluded."""

    source_root: str
    target_database: str
    project_mapping: dict[str, str]
    entries: tuple[ManifestEntry, ...]
    anchors: tuple[dict[str, str], ...]
    project_caches: tuple[dict[str, str], ...]
    fingerprint: str
    source_schema: int = 1
    target_schema: int = 2

    def receipt(self) -> dict[str, Any]:
        entries = self.entries
        return {
            "plan_fingerprint": self.fingerprint,
            "schema_versions": {
                "source": self.source_schema,
                "target": self.target_schema,
            },
            "source_entries": len(entries),
            "source_digests": {
                item.relative_path: item.sha256 for item in entries
            },
            "planned_states": _state_counts(entries),
            "downgraded": {
                item.relative_path: item.downgrade_reason
                for item in entries
                if item.downgrade_reason
            },
            "anchors": len(self.anchors),
            "project_caches": len(self.project_caches),
            "source_retention": {"mode": "READ_ONLY", "retain_days": 30},
        }


def build_plan(
    source_root: Path,
    target_database: Path,
    project_mapping: dict[str, str],
) -> MigrationPlan:
    """Build explicit plan. Unknown partitions become visible candidates."""
    source = _safe_directory(source_root)
    target = target_database.absolute()
    _validate_target(source, target)
    mapping = _mapping(project_mapping)
    entries = _entries(source, mapping)
    anchors = _anchors(source, mapping)
    caches = _project_caches(source, mapping)
    payload = {
        "source": str(source),
        "target": str(target),
        "mapping": mapping,
        "entries": [asdict(item) for item in entries],
        "anchors": anchors,
        "caches": caches,
        "source_schema": 1,
        "target_schema": 2,
    }
    canonical = json.dumps(payload, sort_keys=True, separators=(",", ":"))
    fingerprint = _digest(canonical)
    return MigrationPlan(
        str(source), str(target), mapping, tuple(entries), tuple(anchors),
        tuple(caches), fingerprint,
    )


def verify_source(plan: MigrationPlan) -> None:
    """Reject apply if plan inputs changed after manifest construction."""
    current = build_plan(
        Path(plan.source_root), Path(plan.target_database), plan.project_mapping
    )
    if current.fingerprint != plan.fingerprint:
        raise MigrationSafetyError("legacy source changed after preflight")


def _safe_directory(path: Path) -> Path:
    if str(path).startswith("\\\\"):
        raise MigrationSafetyError("source root cannot be UNC")
    try:
        resolved = path.resolve(strict=True)
    except OSError as error:
        raise MigrationSafetyError("source root unavailable") from error
    if not resolved.is_dir() or _is_link(resolved):
        raise MigrationSafetyError("source root must be ordinary directory")
    return resolved


def _validate_target(source: Path, target: Path) -> None:
    if str(target).startswith("\\\\"):
        raise MigrationSafetyError("target cannot be UNC")
    for parent in (target.parent, *target.parents):
        if parent.exists() and _is_link(parent):
            raise MigrationSafetyError(
                "target parent cannot be link or junction"
            )
    try:
        target.relative_to(source)
    except ValueError:
        if target.exists() and not stat.S_ISREG(target.stat().st_mode):
            raise MigrationSafetyError("target must be ordinary database file")
    else:
        raise MigrationSafetyError("target cannot be nested in source")


def _mapping(value: object) -> dict[str, str]:
    if not isinstance(value, dict) or not value:
        raise MigrationSafetyError("project mapping is required")
    result = {str(key): str(alias) for key, alias in value.items()}
    if any(
        not key.strip() or not alias.strip() for key, alias in result.items()
    ):
        raise MigrationSafetyError("project mapping contains empty identity")
    if len(set(result.values())) != len(result):
        raise MigrationSafetyError("project mapping aliases must be isolated")
    return dict(sorted(result.items()))


def _entries(source: Path, mapping: dict[str, str]) -> list[ManifestEntry]:
    entries: list[ManifestEntry] = []
    for path in sorted(source.rglob("*")):
        relative = path.relative_to(source).as_posix()
        root = relative.split("/", 1)[0]
        if root in METADATA_ROOTS:
            _reject_metadata_link(path)
            continue
        if not path.is_file():
            _reject_non_file(path)
            continue
        partition, state, reason = _partition(relative)
        entries.append(
            _inspect_entry(source, path, partition, state, reason, mapping)
        )
    return entries


def _partition(relative: str) -> tuple[str, str, str | None]:
    for partition, state in PARTITIONS.items():
        if relative == partition or relative.startswith(f"{partition}/"):
            return partition, state, None
    return "unknown", "candidate", "unknown partition downgraded to candidate"


def _inspect_entry(
    source: Path,
    path: Path,
    partition: str,
    state: str,
    reason: str | None,
    mapping: dict[str, str],
) -> ManifestEntry:
    try:
        inspected = inspect_input(source, path)[0]
        inspected.content.decode("utf-8")
    except IntakeSecurityError as error:
        raise MigrationSafetyError(
            "sensitive or unsafe legacy input"
        ) from error
    except UnicodeDecodeError as error:
        raise MigrationSafetyError("legacy input must be UTF-8 text") from error
    project = _project_for(inspected.relative_path, mapping)
    return ManifestEntry(
        inspected.relative_path, partition, state, inspected.size_bytes,
        inspected.modified_ns, inspected.sha256, project, reason,
    )


def _anchors(source: Path, mapping: dict[str, str]) -> list[dict[str, str]]:
    result: list[dict[str, str]] = []
    for path in sorted((source / "sessions").glob("*/anchor.json")):
        _reject_metadata_link(path)
        try:
            data = json.loads(path.read_text(encoding="utf-8"))
            project, key = str(data["project_id"]), str(data["session_key"])
        except (OSError, KeyError, TypeError, json.JSONDecodeError) as error:
            raise MigrationSafetyError("invalid session anchor") from error
        if project not in mapping or not key or len(key) > 128:
            raise MigrationSafetyError("anchor project mapping is missing")
        result.append(
            {
                "legacy_project": project,
                "project_alias": mapping[project],
                "session_key": key,
            }
        )
    return result


def _project_caches(
    source: Path, mapping: dict[str, str]
) -> list[dict[str, str]]:
    result: list[dict[str, str]] = []
    for project, alias in sorted(mapping.items()):
        manifest = source / "projects" / project / "manifest.json"
        if not manifest.exists():
            continue
        _reject_metadata_link(manifest)
        try:
            digest = _digest(manifest.read_text(encoding="utf-8"))
        except OSError as error:
            raise MigrationSafetyError(
                "legacy project cache unavailable"
            ) from error
        result.append(
            {
                "legacy_project": project,
                "project_alias": alias,
                "manifest_sha256": digest,
            }
        )
    return result


def _project_for(relative: str, mapping: dict[str, str]) -> str:
    parts = Path(relative).parts
    if len(parts) >= 3 and parts[0] == "projects" and parts[1] in mapping:
        return mapping[parts[1]]
    if len(mapping) != 1:
        raise MigrationSafetyError("document ownership cannot be proven")
    return next(iter(mapping.values()))


def _reject_metadata_link(path: Path) -> None:
    if _is_link(path):
        raise MigrationSafetyError("legacy links and junctions are forbidden")


def _reject_non_file(path: Path) -> None:
    _reject_metadata_link(path)
    if not path.is_dir():
        raise MigrationSafetyError("legacy special file is forbidden")


def _is_link(path: Path) -> bool:
    junction = getattr(path, "is_junction", lambda: False)
    return path.is_symlink() or bool(junction())


def _digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _state_counts(entries: tuple[ManifestEntry, ...]) -> dict[str, int]:
    return {
        state: sum(item.state == state for item in entries)
        for state in PARTITIONS.values()
    }
