from __future__ import annotations

import io
import json
import sqlite3
from contextlib import redirect_stdout
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import pytest

from codex_bootstrap_import import (
    _safe_absolute,
    import_codex_candidates,
)
from setup import main
from tests.test_codex_bootstrap_installer import FakeDependencies, _write


def test_absent_source_skips_without_creating_target(tmp_path: Path) -> None:
    target = tmp_path / "knowledge"
    result = import_codex_candidates(
        tmp_path / "missing-codex",
        target,
        "auto",
        dependencies=FakeDependencies(tmp_path).value,
    )
    assert result["status"] == "SKIPPED_NO_SOURCE"
    assert not target.exists()


def test_secret_scan_failure_writes_no_candidate_or_marker(
    tmp_path: Path,
) -> None:
    codex = tmp_path / "codex"
    knowledge = tmp_path / "knowledge"
    secret = "api_key = '0123456789abcdefghijklmnop'"
    _write(codex / "mem.md", secret)

    result = import_codex_candidates(codex, knowledge, "force")

    assert result["status"] == "FAILED"
    assert result["blocked_count"] == 1
    assert secret not in json.dumps(result)
    database = knowledge / "service" / "knowledge.sqlite3"
    with sqlite3.connect(database) as current:
        assert current.execute(
            "SELECT COUNT(*) FROM documents"
        ).fetchone()[0] == 0
        assert current.execute(
            "SELECT COUNT(*) FROM import_receipts"
        ).fetchone()[0] == 0


def test_distinct_codex_roots_keep_separate_provenance(
    tmp_path: Path,
) -> None:
    first_root = tmp_path / "first-codex"
    second_root = tmp_path / "second-codex"
    knowledge = tmp_path / "knowledge"
    _write(first_root / "mem.md", "shared memory")
    _write(second_root / "mem.md", "shared memory")

    first = import_codex_candidates(first_root, knowledge, "auto")
    second = import_codex_candidates(second_root, knowledge, "auto")

    assert first["imported_count"] == 1
    assert second["imported_count"] == 0
    assert second["deduplicated_count"] == 1
    database = knowledge / "service" / "knowledge.sqlite3"
    with sqlite3.connect(database) as current:
        assert current.execute(
            "SELECT COUNT(*) FROM documents"
        ).fetchone()[0] == 1
        assert current.execute(
            "SELECT COUNT(*) FROM import_provenance"
        ).fetchone()[0] == 2
        assert current.execute(
            "SELECT COUNT(*) FROM import_receipts"
        ).fetchone()[0] == 2


def test_receipt_hides_source_paths_names_and_content(tmp_path: Path) -> None:
    codex = tmp_path / "private-user" / "codex"
    _write(codex / "mem.md", "private-content-marker")
    receipt = import_codex_candidates(
        codex,
        tmp_path / "knowledge",
        "force",
        dependencies=FakeDependencies(codex).value,
    )
    encoded = json.dumps(receipt)
    assert "private-user" not in encoded
    assert "mem.md" not in encoded
    assert "private-content-marker" not in encoded
    assert receipt["database_scope"] == "global-candidates"


def test_unc_and_reparse_inputs_fail_closed(tmp_path: Path) -> None:
    with pytest.raises(RuntimeError, match="UNC"):
        _safe_absolute(Path(r"\\server\share\codex"), must_exist=False)

    codex = tmp_path / "codex"
    codex.mkdir()
    linked = codex / "memories"
    linked.mkdir()
    fake_stat = SimpleNamespace(st_file_attributes=0x400)
    original_lstat = Path.lstat

    def lstat(path: Path) -> object:
        if path == linked:
            return fake_stat
        return original_lstat(path)

    with (
        mock.patch.object(Path, "lstat", lstat),
        mock.patch.object(Path, "is_symlink", lambda path: False),
        mock.patch.object(Path, "is_junction", lambda path: False),
    ):
        result = import_codex_candidates(
            codex,
            tmp_path / "knowledge",
            "force",
            dependencies=FakeDependencies(codex).value,
        )
    assert result["status"] == "FAILED"
    assert result["failure_code"] == "SOURCE_BLOCKED"
    assert result["blocked_count"] == 1


def test_source_file_and_overlapping_target_fail_closed(
    tmp_path: Path,
) -> None:
    source_file = tmp_path / "codex-file"
    source_file.write_text("not a directory", encoding="utf-8")
    file_result = import_codex_candidates(
        source_file,
        tmp_path / "knowledge",
        "force",
        dependencies=FakeDependencies(tmp_path).value,
    )
    assert file_result["status"] == "FAILED"
    assert file_result["failure_stage"] == "SOURCE_ROOT"
    assert file_result["failure_code"] == "SOURCE_BLOCKED"

    codex = tmp_path / "codex"
    codex.mkdir()
    overlap_result = import_codex_candidates(
        codex,
        codex / "memories" / "service-target",
        "force",
        dependencies=FakeDependencies(codex).value,
    )
    assert overlap_result["status"] == "FAILED"
    assert overlap_result["failure_stage"] == "TARGET_ROOT"
    assert overlap_result["failure_code"] == "SOURCE_BLOCKED"
    assert not (codex / "memories" / "service-target").exists()

    dotted_source = codex / "nested" / ".."
    dotted_result = import_codex_candidates(
        dotted_source,
        codex / "knowledge-target",
        "force",
        dependencies=FakeDependencies(codex).value,
    )
    assert dotted_result["status"] == "FAILED"
    assert dotted_result["failure_code"] == "SOURCE_BLOCKED"
    assert not (codex / "knowledge-target").exists()


def test_sensitive_filename_families_are_never_opened(
    tmp_path: Path,
) -> None:
    codex = tmp_path / "codex"
    knowledge = tmp_path / "knowledge"
    names = (
        "auth.md", "config.json", "sessions.txt", "history.md",
        "logs.txt", "cache.md", "tmp.txt", "plugins.md", "skills.md",
        "worktrees.md", "artifacts.md", "DB.md", "archive.md",
        "credentials.txt", "token.backup.json", "secret.notes.md",
        "session.txt", "log.md", "plugin.md", "skill.md", "worktree.md",
        "artifact.txt", "credentials_backup.md", "session-data.json",
    )
    for filename in names:
        _write(codex / "memories" / filename, "must not be opened")
    fake = FakeDependencies(codex)

    result = import_codex_candidates(
        codex, knowledge, "force", dependencies=fake.value
    )

    assert result["status"] == "SUCCEEDED"
    assert result["discovered_count"] == 0
    assert result["excluded_count"] == len(names)
    assert fake.inspected == []


def test_setup_receipt_never_exposes_target_path(tmp_path: Path) -> None:
    target = tmp_path / "private-user-target"
    arguments = [
        "--apply", "--yes", "--mode", "knowledge-only",
        "--target-root", str(target), "--codex-import", "off", "--json",
    ]
    output = io.StringIO()
    with redirect_stdout(output):
        assert main(arguments) == 0
    first = json.loads(output.getvalue())
    installed = target / "skills" / "ytqjk-knowledge" / "SKILL.md"
    installed.write_text("changed", encoding="utf-8")

    output = io.StringIO()
    with redirect_stdout(output):
        assert main(arguments) == 0
    upgraded = json.loads(output.getvalue())
    encoded = json.dumps((first, upgraded))
    snapshot = str(upgraded["apply"]["snapshot"])

    assert str(target) not in encoded
    assert target.name not in encoded
    assert first["target_root"] == "CONFIGURED"
    assert first["copies"] == ["ytqjk-knowledge"]
    assert snapshot.startswith(".ytqjk-install/snapshots/")
    assert not Path(snapshot).is_absolute()
    assert (target / snapshot).is_dir()
