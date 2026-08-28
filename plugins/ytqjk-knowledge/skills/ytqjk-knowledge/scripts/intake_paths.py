"""Cross-version filesystem path safety helpers."""

from __future__ import annotations

import stat
from pathlib import Path


def is_link_or_junction(path: Path) -> bool:
    """Return whether a path is a symlink, junction, or reparse point."""
    if path.is_symlink():
        return True
    junction_check = getattr(path, "is_junction", None)
    if junction_check is not None and junction_check():
        return True
    try:
        attributes = path.lstat().st_file_attributes
    except (AttributeError, OSError):
        return False
    reparse_flag = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0)
    return bool(attributes & reparse_flag)
