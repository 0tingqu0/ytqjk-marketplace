from __future__ import annotations

import sys
from pathlib import Path

import pytest


DASHBOARD = Path(__file__).resolve().parents[2] / "dashboard"
SCRIPTS = DASHBOARD.parent / "scripts"
sys.path.insert(0, str(SCRIPTS))
sys.path.insert(0, str(DASHBOARD))

from candidate_actions import (  # noqa: E402
    candidate_version,
    update_candidate,
)


def test_stale_candidate_edit_is_rejected_without_overwrite(
    tmp_path: Path,
) -> None:
    relative = "personal-experience/candidates/conflict.md"
    candidate = tmp_path / relative
    candidate.parent.mkdir(parents=True)
    candidate.write_text(
        "---\nstatus: CANDIDATE\n---\n\nfirst\n",
        encoding="utf-8",
    )
    original = candidate.read_text(encoding="utf-8")
    original_version = candidate_version(original)
    changed = original.replace("first", "second")

    result = update_candidate(
        tmp_path, relative, changed, original_version,
    )

    assert result["version"] == candidate_version(changed)
    with pytest.raises(ValueError, match="CANDIDATE_VERSION_CONFLICT"):
        update_candidate(
            tmp_path,
            relative,
            original.replace("first", "stale overwrite"),
            original_version,
        )
    assert candidate.read_text(encoding="utf-8") == changed
