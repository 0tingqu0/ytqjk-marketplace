"""Integrity policy for the isolated CPU document runtime."""

from __future__ import annotations

import hashlib
import json
import os
import re
from pathlib import Path

from path_safety import is_reparse
from stable_file import snapshot_tree


MARKER_NAME = ".ytqjk-runtime.json"
_VOLATILE_DIRECTORIES = frozenset({".cache", ".pytest_cache"})
_BYTECODE_NAME = re.compile(
    r"^(?P<stem>.+)\.[A-Za-z0-9_]+-\d+(?:\.opt-\d+)?\.pyc$"
)
_GPU_EXACT = frozenset({
    "onnxruntime-gpu",
    "pytorch-triton",
    "tensorflow-gpu",
    "triton",
})
_GPU_PREFIXES = ("cuda-", "cupy-cuda", "nvidia-", "tensorrt")


class RuntimeIntegrityError(RuntimeError):
    """Stable fail-closed integrity code."""


def validate_cpu_probe(
    value: object,
    versions: dict[str, str],
) -> dict[str, str]:
    valid = type(value) is dict and set(value) == {
        "packages",
        "distributions",
        "imports",
        "onnx_providers",
    }
    packages = value.get("packages") if type(value) is dict else None
    distributions = (
        value.get("distributions") if type(value) is dict else None
    )
    imports = value.get("imports") if type(value) is dict else None
    providers = (
        value.get("onnx_providers") if type(value) is dict else None
    )
    valid = valid and type(packages) is dict
    valid = valid and set(packages) == set(versions)
    valid = valid and all(
        type(item) is str and item for item in packages.values()
    )
    valid = valid and type(distributions) is list
    valid = valid and all(
        type(item) is str and item for item in distributions
    )
    valid = valid and type(imports) is list
    valid = valid and all(type(item) is str and item for item in imports)
    valid = valid and type(providers) is list
    valid = valid and all(type(item) is str and item for item in providers)
    if not valid:
        raise RuntimeIntegrityError("PACKAGE_PROBE_INVALID")
    if forbidden_gpu_distributions(distributions):
        raise RuntimeIntegrityError("GPU_DISTRIBUTION_PRESENT")
    if any(packages[name] != version for name, version in versions.items()):
        raise RuntimeIntegrityError("PACKAGE_VERSION_MISMATCH")
    if len(imports) != len(set(imports)) or set(imports) != set(versions):
        raise RuntimeIntegrityError("PACKAGE_IMPORT_FAILED")
    if "CPUExecutionProvider" not in providers:
        raise RuntimeIntegrityError("ONNX_CPU_PROVIDER_MISSING")
    return dict(packages)


def runtime_tree_sha256(
    root: Path,
    max_files: int,
    max_bytes: int,
) -> str:
    excluded = _runtime_cache_files(root)
    tree = snapshot_tree(
        root,
        max_files,
        max_bytes,
        excluded=frozenset(excluded),
    )
    content = json.dumps(
        tree.hashes,
        ensure_ascii=True,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return hashlib.sha256(content).hexdigest()


def forbidden_gpu_distributions(names: list[str]) -> list[str]:
    normalized = {_canonical_name(name) for name in names}
    return sorted(
        name
        for name in normalized
        if name in _GPU_EXACT or name.startswith(_GPU_PREFIXES)
    )


def _runtime_cache_files(root: Path) -> set[str]:
    excluded = {MARKER_NAME}
    for current, names, files in os.walk(root, followlinks=False):
        parent = Path(current)
        relative_parent = parent.relative_to(root)
        volatile = any(
            part.casefold() in _VOLATILE_DIRECTORIES
            for part in relative_parent.parts
        )
        for name in files:
            if volatile or _is_compliant_bytecode_cache(parent, name):
                excluded.add((relative_parent / name).as_posix())
        names.sort()
    return excluded


def _is_compliant_bytecode_cache(parent: Path, name: str) -> bool:
    if parent.name.casefold() != "__pycache__":
        return False
    match = _BYTECODE_NAME.fullmatch(name)
    if match is None:
        return False
    source = parent.parent / f"{match.group('stem')}.py"
    return source.is_file() and not is_reparse(source)


def _canonical_name(name: str) -> str:
    return re.sub(r"[-_.]+", "-", name.strip()).casefold()
