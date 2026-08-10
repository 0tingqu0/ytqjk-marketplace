from __future__ import annotations

import json
import sys
from typing import Any


def write_json(value: Any, *, indent: int | None = None) -> None:
    text = json.dumps(value, ensure_ascii=False, indent=indent) + "\n"
    stream = getattr(sys.stdout, "buffer", None)
    if stream is None:
        sys.stdout.write(text)
        return
    stream.write(text.encode("utf-8"))
    stream.flush()
