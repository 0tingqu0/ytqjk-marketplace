from __future__ import annotations

from pathlib import Path, PurePosixPath
from typing import Any, Callable, Sequence


GitRunner = Callable[..., Any]


def rollback_apply(
    repo: Path,
    changed: Sequence[str],
    tracked: Sequence[str],
    untracked: Sequence[str],
    git_runner: GitRunner,
) -> list[str]:
    errors: list[str] = []
    pathspecs = [f":(top,literal){path}" for path in changed]
    reset = git_runner(repo, "reset", "--quiet", "HEAD", "--", *pathspecs)
    if reset.returncode != 0:
        errors.append("index reset failed")
    if tracked:
        tracked_specs = [f":(top,literal){path}" for path in tracked]
        restore = git_runner(
            repo, "restore", "--source=HEAD", "--worktree", "--", *tracked_specs
        )
        if restore.returncode != 0:
            errors.append("tracked worktree restore failed")
    for relative in untracked:
        target = repo.joinpath(*PurePosixPath(relative).parts)
        try:
            if target.is_file() or target.is_symlink():
                target.unlink()
            elif target.exists():
                errors.append(f"unexpected rollback target: {relative}")
        except OSError as exc:
            errors.append(f"untracked cleanup failed for {relative}: {exc}")
    status = git_runner(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
    if status.returncode != 0 or status.stdout:
        errors.append("integration worktree is not clean after rollback")
    return errors
