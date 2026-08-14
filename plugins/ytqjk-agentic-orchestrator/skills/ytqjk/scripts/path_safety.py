from __future__ import annotations

import os
import stat
from pathlib import Path


def is_reparse(path: Path) -> bool:
    if path.is_symlink():
        return True
    is_junction = getattr(os.path, "isjunction", None)
    if is_junction and is_junction(path):
        return True
    try:
        attributes = path.stat(follow_symlinks=False).st_file_attributes
    except (AttributeError, OSError):
        return False
    flag = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    return bool(attributes & flag)


def is_direct_directory(path: Path, parent: Path) -> bool:
    try:
        return (
            parent.is_dir()
            and not is_reparse(parent)
            and path.is_dir()
            and not is_reparse(path)
            and path.resolve().parent == parent.resolve()
        )
    except OSError:
        return False
