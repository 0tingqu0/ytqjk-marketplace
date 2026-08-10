from __future__ import annotations

import argparse
import json
import subprocess
import venv
from pathlib import Path

from json_output import write_json
from platform_paths import default_knowledge_root, runtime_python


REQUIRED = {"lancedb": "0.34.0", "fastembed": "0.8.0"}


def installed_versions(python: Path) -> dict[str, str]:
    code = (
        "import importlib.metadata,json;"
        "print(json.dumps({n:importlib.metadata.version(n) "
        "for n in ['lancedb','fastembed']}))"
    )
    result = subprocess.run(
        [str(python), "-c", code], capture_output=True, text=True, check=False
    )
    if result.returncode != 0:
        return {}
    return json.loads(result.stdout)


def ensure_runtime(root: Path, check_only: bool) -> dict[str, object]:
    runtime_dir = root / ".runtime"
    python = runtime_python(runtime_dir)
    current = installed_versions(python) if python.exists() else {}
    ready = current == REQUIRED
    if check_only or ready:
        return {"ready": ready, "python": str(python), "versions": current}

    root.mkdir(parents=True, exist_ok=True)
    if not python.exists():
        venv.EnvBuilder(with_pip=True, clear=False).create(runtime_dir)
    requirements = Path(__file__).with_name("requirements-vector.txt")
    subprocess.run(
        [str(python), "-m", "pip", "install", "--requirement", str(requirements)],
        check=True,
    )
    current = installed_versions(python)
    if current != REQUIRED:
        raise RuntimeError(f"Unexpected installed versions: {current}")
    return {"ready": True, "python": str(python), "versions": current}


def main() -> int:
    parser = argparse.ArgumentParser(description="Prepare isolated local RAG runtime.")
    parser.add_argument("--knowledge-root", type=Path, default=default_knowledge_root())
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    try:
        result = ensure_runtime(args.knowledge_root.resolve(), args.check)
    except (OSError, subprocess.CalledProcessError, RuntimeError) as exc:
        write_json({"ready": False, "error": str(exc)})
        return 1
    write_json(result, indent=2)
    return 0 if result["ready"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
