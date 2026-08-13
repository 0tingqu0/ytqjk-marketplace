"""Strict, secret-safe CLI boundary for YTQJK identity control-plane."""

from __future__ import annotations

import json
import sys
from pathlib import Path

from orchestration_identity import IdentityError, OrchestrationControlPlane


_FLAGS = {"--database", "--key-file", "--project-id", "--objective-hash", "--session-key"}
_REQUIRED = {"--database", "--key-file", "--project-id", "--objective-hash", "--session-key"}


def main() -> int:
    """Run only exact supported argument grammar; never echo rejected input."""
    values = _parse(sys.argv[1:])
    if values is None:
        return _output(False, "invalid identity command")
    control = OrchestrationControlPlane(
        Path(values["--database"]), Path(values["--key-file"])
    )
    try:
        database_id = control.initialize()
        run_id = control.start_run(
            values["--project-id"],
            values["--objective-hash"],
            values["--session-key"],
            values["--session-key"],
        )
        return _output(True, "run started", run_id=run_id, database_id=database_id)
    except (IdentityError, ValueError):
        return _output(False, "invalid identity input")


def _parse(arguments: list[str]) -> dict[str, str] | None:
    """Accept only exact fixed-order grammar before any value is interpreted."""
    grammar = (
        "--database", None, "--key-file", None, "start-run", "--project-id", None,
        "--objective-hash", None, "--session-key", None,
    )
    if len(arguments) != len(grammar):
        return None
    values = {}
    for index, expected in enumerate(grammar):
        if expected is not None and arguments[index] != expected:
            return None
        if expected is None and (
            not arguments[index] or arguments[index] == "start-run" or arguments[index].startswith("--")
        ):
            return None
    for flag_index in (0, 2, 5, 7, 9):
        values[arguments[flag_index]] = arguments[flag_index + 1]
    return values if set(values) == _REQUIRED else None


def _output(ok: bool, status: str, **extra: str) -> int:
    print(json.dumps({"ok": ok, "status": status, **extra}, ensure_ascii=False))
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
