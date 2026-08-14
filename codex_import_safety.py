"""Path-only security checks for Codex bootstrap discovery."""
from __future__ import annotations

import os
import stat
from pathlib import Path


EXCLUDED_NAMES = frozenset(
    {
        "artifact", "artifacts", "auth", "archive", "archives", "cache",
        "config", "db", "database", "databases", "history", "log", "logs",
        "plugin", "plugins", "session", "sessions", "skill", "skills",
        "tmp", "worktree", "worktrees",
    }
)
SENSITIVE_NAMES = frozenset(
    {
        ".authinfo", ".aws", ".azure", ".cargo", ".docker", ".env",
        ".git-credentials", ".gnupg", ".kube", ".m2", ".netrc",
        ".npmrc", ".ssh", ".terraform", ".venv", "auth.json",
        "credentials", "credentials.json", "secret.json", "secrets",
        "secrets.json", "token.json", "tokens.json",
    }
)
SENSITIVE_STEMS = frozenset(
    {
        "auth", "credential", "credentials", "secret", "secrets",
        "token", "tokens",
    }
)
SENSITIVE_ENDINGS = tuple(
    (
        ".age .asc .gpg .jks .kdbx .key .keystore .p12 .pem .pfx "
        ".tfstate .tfstate.backup .tfvars .tfvars.json"
    ).split()
)
REPARSE_ATTRIBUTE = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)


class SourceBlockedError(RuntimeError):
    """Raised when an allowlisted source fails path security checks."""


def excluded_name(name: str) -> bool:
    """Return whether a path component must never be opened."""
    folded = name.casefold()
    stem = folded.partition(".")[0]
    family = stem.partition("-")[0].partition("_")[0]
    return (
        folded in EXCLUDED_NAMES
        or folded in SENSITIVE_NAMES
        or stem in EXCLUDED_NAMES
        or stem in SENSITIVE_STEMS
        or family in EXCLUDED_NAMES
        or family in SENSITIVE_STEMS
        or folded.startswith(".env.")
        or folded.endswith(SENSITIVE_ENDINGS)
    )


def safe_absolute(path: Path, *, must_exist: bool) -> Path:
    """Build an absolute local path without following unsafe links."""
    expanded = path.expanduser()
    if str(expanded).startswith(("\\\\", "//")):
        raise SourceBlockedError("UNC paths are forbidden")
    absolute = Path(os.path.abspath(os.fspath(expanded)))
    current = Path(absolute.anchor)
    for part in absolute.parts[1:]:
        current /= part
        if current.exists() or current.is_symlink():
            reject_link(current)
    if must_exist and not absolute.exists():
        raise SourceBlockedError("required path is unavailable")
    return absolute


def paths_overlap(first: Path, second: Path) -> bool:
    """Return whether either local path contains the other."""
    return first.is_relative_to(second) or second.is_relative_to(first)


def reject_link(path: Path) -> None:
    """Reject symlinks, junctions, and other reparse points."""
    try:
        attributes = getattr(path.lstat(), "st_file_attributes", 0)
    except OSError as error:
        raise SourceBlockedError("input metadata is unavailable") from error
    junction = getattr(path, "is_junction", lambda: False)()
    if path.is_symlink() or junction or attributes & REPARSE_ATTRIBUTE:
        raise SourceBlockedError("links and reparse points are forbidden")
