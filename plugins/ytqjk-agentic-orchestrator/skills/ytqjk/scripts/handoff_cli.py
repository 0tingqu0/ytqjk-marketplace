from __future__ import annotations

import argparse
import json
from pathlib import Path

from handoff_core import HandoffError, apply_bundle, export_bundle


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(
        description="Export and apply reviewed YTQJK handoff bundles."
    )
    commands = result.add_subparsers(dest="command", required=True)
    export = commands.add_parser("export")
    export.add_argument("--repo", type=Path, required=True)
    export.add_argument("--bundle", type=Path, required=True)
    export.add_argument("--path", action="append", required=True, dest="paths")
    apply = commands.add_parser("apply")
    apply.add_argument("--repo", type=Path, required=True)
    apply.add_argument("--bundle", type=Path, required=True)
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        output = (
            export_bundle(args.repo, args.bundle, args.paths)
            if args.command == "export"
            else apply_bundle(args.repo, args.bundle)
        )
    except (HandoffError, OSError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False))
        return 1
    print(json.dumps({"ok": True, **output}, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
