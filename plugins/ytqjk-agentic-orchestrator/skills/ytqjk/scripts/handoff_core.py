from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path, PurePosixPath
from typing import Any, Sequence

from handoff_transaction import rollback_apply


FORMAT = "ytqjk-handoff-v1"
HandoffError = RuntimeError


def run_git(repo: Path, *args: str) -> subprocess.CompletedProcess[bytes]:
    environment = {**os.environ, "LANG": "C", "LC_ALL": "C"}
    return subprocess.run(
        ["git", "-C", str(repo), *args],
        capture_output=True,
        check=False,
        env=environment,
    )


def git_output(repo: Path, *args: str) -> bytes:
    result = run_git(repo, *args)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout).decode("utf-8", errors="replace").strip()
        raise HandoffError(detail or f"git {' '.join(args)} failed")
    return result.stdout


def repo_root(path: Path) -> Path:
    output = git_output(path, "rev-parse", "--show-toplevel")
    return Path(output.decode("utf-8").strip()).resolve()


def normalize_path(value: str) -> str:
    candidate = value.replace("\\", "/")
    path = PurePosixPath(candidate)
    if not candidate or path.is_absolute() or any(part == ".." for part in path.parts):
        raise HandoffError(f"Unsafe repository path: {value!r}")
    if path.parts and path.parts[0].endswith(":"):
        raise HandoffError(f"Unsafe repository path: {value!r}")
    if any(part.casefold() == ".git" for part in path.parts):
        raise HandoffError(f"Git metadata is not a handoff path: {value!r}")
    normalized = path.as_posix()
    if normalized in {"", "."}:
        raise HandoffError(f"Unsafe repository path: {value!r}")
    return normalized


def decode_paths(output: bytes) -> list[str]:
    try:
        return [normalize_path(item.decode("utf-8")) for item in output.split(b"\0") if item]
    except UnicodeDecodeError as exc:
        raise HandoffError("Git returned a path that is not valid UTF-8") from exc


def ensure_outside_repo(repo: Path, bundle: Path) -> Path:
    resolved = bundle.resolve()
    if resolved == repo or repo in resolved.parents:
        raise HandoffError("Handoff bundle must be outside the repository")
    return resolved


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def bundle_digest(manifest: dict[str, Any]) -> str:
    unsigned = dict(manifest)
    unsigned.pop("bundle_sha256", None)
    encoded = json.dumps(
        unsigned, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def ensure_worker_index_clean(repo: Path) -> None:
    status = git_output(
        repo, "status", "--porcelain=v1", "-z", "--untracked-files=no", "--no-renames"
    )
    if any(
        record[:1] != b" " or record[1:2] == b"A"
        for record in status.split(b"\0")
        if record
    ):
        raise HandoffError("Worker index is not clean; workers must not stage changes")
    cached = run_git(repo, "diff", "--cached", "--quiet", "--exit-code", "--")
    if cached.returncode:
        raise HandoffError("Worker index is not clean; workers must not stage changes")
    if git_output(repo, "diff", "--name-only", "--diff-filter=U", "-z", "--"):
        raise HandoffError("Worker has unresolved paths")


def export_bundle(repo_arg: Path, bundle_arg: Path, allowlist: Sequence[str]) -> dict[str, Any]:
    repo = repo_root(repo_arg)
    bundle = ensure_outside_repo(repo, bundle_arg)
    if bundle.exists():
        raise HandoffError(f"Bundle already exists: {bundle}")
    allowed = sorted({normalize_path(path) for path in allowlist})
    if not allowed:
        raise HandoffError("At least one --path allowlist entry is required")
    ensure_worker_index_clean(repo)
    tracked = sorted(
        set(decode_paths(git_output(repo, "diff", "--name-only", "--no-renames", "-z", "--")))
    )
    untracked = sorted(
        set(decode_paths(git_output(repo, "ls-files", "--others", "--exclude-standard", "-z")))
    )
    changed = set(tracked) | set(untracked)
    if not changed:
        raise HandoffError("Worker has no changes to export")
    outside = sorted(changed - set(allowed))
    if outside:
        raise HandoffError(f"Changes outside allowlist: {', '.join(outside)}")
    patch = git_output(
        repo, "diff", "--binary", "--full-index", "--no-color", "--no-ext-diff",
        "--no-renames", "--no-textconv", "--"
    )
    records: list[dict[str, Any]] = []
    for relative in untracked:
        source = repo.joinpath(*PurePosixPath(relative).parts)
        if not source.is_file() or source.is_symlink():
            raise HandoffError(f"Untracked payload is not a regular file: {relative}")
        records.append(
            {"path": relative, "bytes": source.stat().st_size, "sha256": sha256_file(source)}
        )
    manifest: dict[str, Any] = {
        "format": FORMAT,
        "base_head": git_output(repo, "rev-parse", "HEAD").decode("ascii").strip(),
        "allowlist": allowed,
        "tracked": {
            "paths": tracked,
            "patch": "tracked.patch",
            "bytes": len(patch),
            "sha256": hashlib.sha256(patch).hexdigest(),
        },
        "untracked": records,
    }
    manifest["bundle_sha256"] = bundle_digest(manifest)
    bundle.parent.mkdir(parents=True, exist_ok=True)
    temporary = Path(tempfile.mkdtemp(prefix=f".{bundle.name}.", dir=bundle.parent))
    try:
        (temporary / "tracked.patch").write_bytes(patch)
        for record in records:
            relative = str(record["path"])
            source = repo.joinpath(*PurePosixPath(relative).parts)
            target = temporary / "untracked" / Path(*PurePosixPath(relative).parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
        (temporary / "manifest.json").write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        os.replace(temporary, bundle)
    finally:
        if temporary.exists():
            shutil.rmtree(temporary)
    return {
        "bundle": str(bundle),
        "bundle_sha256": manifest["bundle_sha256"],
        "base_head": manifest["base_head"],
        "paths": sorted(changed),
    }


def load_manifest(bundle: Path) -> dict[str, Any]:
    try:
        value = json.loads((bundle / "manifest.json").read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise HandoffError(f"Invalid bundle manifest: {exc}") from exc
    if not isinstance(value, dict) or value.get("format") != FORMAT:
        raise HandoffError("Unsupported bundle format")
    if value.get("bundle_sha256") != bundle_digest(value):
        raise HandoffError("Bundle manifest hash mismatch")
    return value


def validated_payload(bundle: Path, manifest: dict[str, Any]) -> tuple[Path, list[str]]:
    tracked = manifest.get("tracked")
    untracked = manifest.get("untracked")
    allowlist = manifest.get("allowlist")
    path_values = tracked.get("paths") if isinstance(tracked, dict) else None
    if not isinstance(path_values, list) or not all(isinstance(path, str) for path in path_values):
        raise HandoffError("Malformed bundle manifest")
    if not isinstance(untracked, list) or not isinstance(allowlist, list) or not all(isinstance(path, str) for path in allowlist):
        raise HandoffError("Malformed bundle manifest")
    tracked_paths = [normalize_path(path) for path in path_values]
    allowed = {normalize_path(path) for path in allowlist}
    patch = bundle / "tracked.patch"
    if tracked.get("patch") != "tracked.patch" or not patch.is_file() or patch.is_symlink():
        raise HandoffError("Tracked patch is missing or unsafe")
    if tracked.get("bytes") != patch.stat().st_size or tracked.get("sha256") != sha256_file(patch):
        raise HandoffError("Tracked patch hash mismatch")
    records: list[str] = []
    for record in untracked:
        if not isinstance(record, dict) or not isinstance(record.get("path"), str):
            raise HandoffError("Malformed untracked manifest entry")
        relative = normalize_path(str(record.get("path", "")))
        payload = bundle / "untracked" / Path(*PurePosixPath(relative).parts)
        if not payload.is_file() or payload.is_symlink() or bundle not in payload.resolve().parents:
            raise HandoffError(f"Untracked payload is missing or unsafe: {relative}")
        if record.get("bytes") != payload.stat().st_size or record.get("sha256") != sha256_file(payload):
            raise HandoffError(f"Untracked payload hash mismatch: {relative}")
        records.append(relative)
    changed = tracked_paths + records
    if len(changed) != len(set(changed)) or not set(changed) <= allowed:
        raise HandoffError("Bundle paths are duplicated or outside the allowlist")
    return patch, sorted(changed)


def patch_paths(repo: Path, patch: Path) -> list[str]:
    if not patch.stat().st_size:
        return []
    output = git_output(repo, "apply", "--numstat", "-z", str(patch))
    paths: list[str] = []
    for record in output.split(b"\0"):
        if not record:
            continue
        fields = record.split(b"\t", 2)
        if len(fields) != 3:
            raise HandoffError("Tracked patch contains an unsupported path record")
        try:
            paths.append(normalize_path(fields[2].decode("utf-8")))
        except UnicodeDecodeError as exc:
            raise HandoffError("Tracked patch path is not valid UTF-8") from exc
    return sorted(set(paths))


def apply_bundle(repo_arg: Path, bundle_arg: Path) -> dict[str, Any]:
    repo = repo_root(repo_arg)
    bundle = ensure_outside_repo(repo, bundle_arg)
    if git_output(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all"):
        raise HandoffError("Integration worktree must be clean")
    manifest = load_manifest(bundle)
    patch, changed = validated_payload(bundle, manifest)
    base_head = str(manifest.get("base_head", ""))
    if len(base_head) not in {40, 64} or any(char not in "0123456789abcdef" for char in base_head):
        raise HandoffError("Bundle base HEAD is not a full object ID")
    ancestor = run_git(repo, "merge-base", "--is-ancestor", base_head, "HEAD")
    if ancestor.returncode != 0:
        raise HandoffError("Bundle base HEAD is not an ancestor of integration HEAD")
    tracked_paths = sorted(normalize_path(str(path)) for path in manifest["tracked"]["paths"])
    if patch_paths(repo, patch) != tracked_paths:
        raise HandoffError("Tracked patch paths do not match the manifest")
    untracked_paths = sorted(set(changed) - set(tracked_paths))
    for relative in untracked_paths:
        target = repo.joinpath(*PurePosixPath(relative).parts)
        if target.exists() or target.is_symlink():
            raise HandoffError(f"Untracked target already exists: {relative}")
        if repo not in target.resolve().parents:
            raise HandoffError(f"Untracked target escapes the repository: {relative}")
        ignored = run_git(repo, "check-ignore", "--quiet", "--no-index", "--", relative)
        if ignored.returncode == 0:
            raise HandoffError(f"Untracked target is ignored: {relative}")
        if ignored.returncode != 1:
            raise HandoffError(f"Unable to check ignore rules for: {relative}")
        for parent in target.parents:
            if parent == repo:
                break
            if parent.is_symlink() or (parent.exists() and not parent.is_dir()):
                raise HandoffError(f"Untracked target has an unsafe parent: {relative}")
        source = bundle / "untracked" / Path(*PurePosixPath(relative).parts)
        filtered = run_git(repo, "hash-object", f"--path={relative}", str(source))
        if filtered.returncode != 0:
            detail = (filtered.stderr or filtered.stdout).decode("utf-8", errors="replace").strip()
            raise HandoffError(f"Untracked payload filter failed: {relative}: {detail}")
    if patch.stat().st_size:
        check = run_git(repo, "apply", "--check", "--3way", "--index", "--binary", str(patch))
        check_output = check.stderr + check.stdout
        if check.returncode != 0 or b"with conflicts" in check_output.lower():
            detail = check_output.decode("utf-8", errors="replace").strip()
            raise HandoffError(f"Tracked patch does not apply cleanly: {detail}")
    try:
        if patch.stat().st_size:
            applied = run_git(repo, "apply", "--3way", "--index", "--binary", str(patch))
            if applied.returncode != 0:
                detail = (applied.stderr or applied.stdout).decode(
                    "utf-8", errors="replace"
                ).strip()
                raise HandoffError(f"Tracked patch application failed: {detail}")
        for relative in untracked_paths:
            source = bundle / "untracked" / Path(*PurePosixPath(relative).parts)
            target = repo.joinpath(*PurePosixPath(relative).parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
        untracked_specs = [f":(top,literal){path}" for path in untracked_paths]
        if untracked_specs:
            git_output(repo, "add", "--", *untracked_specs)
        staged = sorted(
            set(decode_paths(git_output(repo, "diff", "--cached", "--name-only", "--no-renames", "-z", "--")))
        )
        if staged != changed:
            raise HandoffError("Staged paths do not match the handoff manifest")
        unstaged = run_git(repo, "diff", "--quiet", "--exit-code", "--")
        if unstaged.returncode != 0 or git_output(
            repo, "ls-files", "--others", "--exclude-standard", "-z"
        ):
            raise HandoffError("Integration produced unexpected unstaged content")
        snapshot = git_output(
            repo, "diff", "--cached", "--binary", "--full-index", "--no-color",
            "--no-ext-diff", "--no-renames", "--no-textconv", "--"
        )
        return {
            "bundle_sha256": manifest["bundle_sha256"],
            "base_head": base_head,
            "integration_head": git_output(repo, "rev-parse", "HEAD").decode("ascii").strip(),
            "staged_paths": staged,
            "staged_snapshot_sha256": hashlib.sha256(snapshot).hexdigest(),
        }
    except (HandoffError, OSError) as exc:
        rollback_errors = rollback_apply(
            repo, changed, tracked_paths, untracked_paths, run_git
        )
        if rollback_errors:
            raise HandoffError(
                f"{exc}; rollback failed: {'; '.join(rollback_errors)}"
            ) from exc
        raise
