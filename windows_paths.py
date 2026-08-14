"""Windows-aware filesystem operations used by installer transactions."""
from __future__ import annotations

import os
from pathlib import Path
import shutil


def remove_tree(path: Path) -> None:
    removable = path
    if os.name == "nt":
        removable = Path("\\\\?\\" + str(path.resolve()))
    shutil.rmtree(removable)
