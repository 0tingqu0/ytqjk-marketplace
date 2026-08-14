from __future__ import annotations

import io
import json
import sqlite3
from contextlib import redirect_stdout
from pathlib import Path
from types import SimpleNamespace

from codex_bootstrap_import import (
    Dependencies,
    default_codex_root,
    import_codex_candidates,
)
from setup import main


def test_codex_root_contract_prefers_explicit_then_environment(
    tmp_path: Path,
) -> None:
    configured = tmp_path / "configured"
    explicit = tmp_path / "explicit"
    environment = {"CODEX_HOME": str(configured)}
    assert default_codex_root(explicit, environment) == explicit
    assert default_codex_root(None, environment) == configured


def test_dry_run_and_off_never_call_importer(tmp_path: Path) -> None:
    def forbidden(*args: object) -> dict[str, object]:
        raise AssertionError("dry-run must not inspect Codex data")

    output = io.StringIO()
    with redirect_stdout(output):
        code = main(
            [
                "--mode", "knowledge-only", "--codex-import", "force",
                "--codex-root", r"\\server\never-read",
                "--knowledge-root", str(tmp_path / "never-read"),
                "--json",
            ],
            codex_importer=forbidden,
        )
    assert code == 0
    assert json.loads(output.getvalue())["knowledge_import"]["status"] == (
        "SKIPPED_DRY_RUN"
    )

    output = io.StringIO()
    with redirect_stdout(output):
        code = main(
            [
                "--apply", "--yes", "--mode", "knowledge-only",
                "--target-root", str(tmp_path / "target"),
                "--codex-import", "off", "--json",
            ],
            codex_importer=forbidden,
        )
    assert code == 0
    assert json.loads(output.getvalue())["knowledge_import"]["status"] == (
        "SKIPPED_OFF"
    )

    ide_target = tmp_path / "ide-target"
    _write(ide_target / "skills" / "grill-me" / "SKILL.md", "fixture")
    output = io.StringIO()
    with redirect_stdout(output):
        code = main(
            [
                "--apply", "--yes", "--mode", "ide-only",
                "--target-root", str(ide_target), "--json",
            ],
            codex_importer=forbidden,
        )
    assert code == 0
    assert json.loads(output.getvalue())["knowledge_import"]["status"] == (
        "SKIPPED_MODE"
    )


def test_apply_calls_import_only_after_success(tmp_path: Path) -> None:
    calls: list[tuple[Path, Path, str]] = []

    def importer(
        codex: Path, knowledge: Path, mode: str
    ) -> dict[str, object]:
        assert (tmp_path / "target" / "skills").is_dir()
        calls.append((codex, knowledge, mode))
        return _receipt("SUCCEEDED", imported=2)

    output = io.StringIO()
    with redirect_stdout(output):
        code = main(
            [
                "--apply", "--yes", "--mode", "knowledge-only",
                "--target-root", str(tmp_path / "target"),
                "--codex-root", str(tmp_path / "codex"),
                "--knowledge-root", str(tmp_path / "knowledge"),
                "--codex-import", "force", "--json",
            ],
            codex_importer=importer,
        )
    data = json.loads(output.getvalue())
    assert code == 0
    assert data["apply"]["status"] == "APPLIED"
    assert data["knowledge_import"]["imported_count"] == 2
    assert calls == [
        (tmp_path / "codex", tmp_path / "knowledge", "force")
    ]
    failed = tmp_path / "failed-target"
    code = main(
        [
            "--apply", "--yes", "--mode", "knowledge-only",
            "--target-root", str(failed), "--fail-after-copy", "--json",
        ],
        codex_importer=importer,
    )
    assert code == 2
    assert len(calls) == 1


def test_import_failure_keeps_apply_and_returns_three(tmp_path: Path) -> None:
    def fail_import(*args: object) -> dict[str, object]:
        raise RuntimeError("private path and content must not escape")

    output = io.StringIO()
    with redirect_stdout(output):
        code = main(
            [
                "--apply", "--yes", "--mode", "knowledge-only",
                "--target-root", str(tmp_path / "target"),
                "--codex-root", str(tmp_path / "codex"),
                "--knowledge-root", str(tmp_path / "knowledge"),
                "--json",
            ],
            codex_importer=fail_import,
        )
    data = json.loads(output.getvalue())
    assert code == 3
    assert data["apply"]["status"] == "APPLIED"
    assert data["knowledge_import"]["status"] == "FAILED"
    assert data["knowledge_import"]["rollback"] == "NOT_APPLICABLE"
    assert data["knowledge_import"]["failure_stage"] == "IMPORTER_CALL"
    assert data["knowledge_import"]["failure_code"] == "IMPORTER_FAILED"
    assert "private path" not in output.getvalue()


def test_allowlist_each_file_scan_marker_and_force(tmp_path: Path) -> None:
    codex = tmp_path / "codex"
    knowledge = tmp_path / "knowledge"
    _write(codex / "mem.md", "memory")
    _write(codex / "memories" / "one.md", "one")
    _write(codex / "memories" / "plain-memory", "plain")
    _write(codex / "knowledge" / "data.json", '{"x": 1}')
    _write(codex / "attachments" / "note.txt", "note")
    _write(codex / "attachments" / "image.png", "unsupported")
    _write(codex / "attachments" / "slides.pptx", "unsupported")
    _write(codex / "memories" / "credentials.json", '{"x": 2}')
    _write(codex / "memories" / "config" / "hidden.md", "hidden")
    _write(codex / "memories" / ".ssh" / "note.md", "never open")
    _write(codex / "memories" / "auth", "never open")
    _write(codex / "memories" / "credentials", "never open")
    _write(codex / "attachments" / "client.pem", "never open")
    fake = FakeDependencies(codex)

    first = import_codex_candidates(
        codex, knowledge, "auto", dependencies=fake.value
    )
    assert first["status"] == "SUCCEEDED"
    assert first["discovered_count"] == 5
    assert first["imported_count"] == 5
    assert first["excluded_count"] == 6
    assert first["not_configured_count"] == 2
    assert len(fake.inspected) == 5
    assert all(root == codex for root, _ in fake.inspected)
    assert fake.service.calls[0][0:2] == ("global", "codex-bootstrap")
    assert str(fake.service.calls[0][2]).startswith("codex-bootstrap-v1-")

    fake.service.marker = SimpleNamespace(
        receipt_sha256="2" * 64,
        created_documents=5,
        deduplicated_documents=0,
        provenance_added=5,
        chunks_created=5,
    )
    skipped = import_codex_candidates(
        codex, knowledge, "auto", dependencies=fake.value
    )
    assert skipped["status"] == "SKIPPED_MARKER"
    assert skipped["scanner"] == "NOT_EXECUTED"
    assert skipped["previous_imported_count"] == 5
    assert skipped["imported_count"] == 0
    assert len(fake.inspected) == 5

    forced = import_codex_candidates(
        codex, knowledge, "force", dependencies=fake.value
    )
    assert forced["status"] == "SUCCEEDED"
    assert forced["imported_count"] == 0
    assert forced["deduplicated_count"] == 5
    assert fake.service.calls[-1][-1] is True


def test_real_adapter_uses_only_temporary_roots(tmp_path: Path) -> None:
    codex = tmp_path / "codex"
    knowledge = tmp_path / "knowledge"
    _write(codex / "mem.md", "safe bootstrap memory")
    _write(codex / "memories" / "extensionless", "plain memory")
    first = import_codex_candidates(codex, knowledge, "auto")
    second = import_codex_candidates(codex, knowledge, "auto")
    forced = import_codex_candidates(codex, knowledge, "force")
    assert first["status"] == "SUCCEEDED"
    assert second["status"] == "SKIPPED_MARKER"
    assert forced["status"] == "SUCCEEDED"
    assert (knowledge / "service" / "knowledge.sqlite3").is_file()


def test_setup_apply_runs_real_import_and_reuses_marker(
    tmp_path: Path,
) -> None:
    codex = tmp_path / "codex"
    knowledge = tmp_path / "knowledge"
    target = tmp_path / "target"
    _write(codex / "mem.md", "end to end memory")
    arguments = [
        "--apply", "--yes", "--mode", "knowledge-only",
        "--target-root", str(target), "--codex-root", str(codex),
        "--knowledge-root", str(knowledge), "--json",
    ]
    output = io.StringIO()
    with redirect_stdout(output):
        first_code = main(arguments)
    first = json.loads(output.getvalue())
    output = io.StringIO()
    with redirect_stdout(output):
        second_code = main(arguments)
    second = json.loads(output.getvalue())
    assert (first_code, first["knowledge_import"]["imported_count"]) == (0, 1)
    assert second_code == 0
    assert second["knowledge_import"]["status"] == "SKIPPED_MARKER"
    database = knowledge / "service" / "knowledge.sqlite3"
    with sqlite3.connect(database) as current:
        assert current.execute(
            "SELECT COUNT(*) FROM documents"
        ).fetchone()[0] == 1


class FakeDependencies:
    def __init__(self, root: Path) -> None:
        self.root = root
        self.inspected: list[tuple[Path, Path]] = []
        self.service = FakeService()
        self.value = Dependencies(
            self.inspect,
            lambda **kwargs: SimpleNamespace(parse=self.parse),
            lambda database: self.service,
            lambda **values: SimpleNamespace(**values),
        )

    def inspect(self, root: Path, path: Path) -> tuple[object, ...]:
        self.inspected.append((root, path))
        relative = path.relative_to(self.root).as_posix()
        digest = relative.encode("utf-8").hex().ljust(64, "0")[:64]
        scan = SimpleNamespace(scanner="local-pattern-v1")
        return (
            SimpleNamespace(
                sha256=digest,
                relative_path=relative,
                scan=scan,
            ),
        )

    def parse(self, source: object) -> object:
        digest = source.sha256
        scan = SimpleNamespace(sha256=digest)
        chunk = SimpleNamespace(text="safe", sha256=digest)
        return SimpleNamespace(
            content_sha256=digest,
            output_scan=scan,
            chunks=(chunk,),
            source=source,
        )


class FakeService:
    def __init__(self) -> None:
        self.marker: object | None = None
        self.calls: list[tuple[object, ...]] = []

    def import_receipt(self, marker: str) -> object | None:
        return self.marker

    def import_candidates(
        self, scope: str, alias: str, marker: str,
        candidates: tuple[object, ...], *, force: bool,
    ) -> object:
        self.calls.append((scope, alias, marker, candidates, force))
        return SimpleNamespace(
            status="IMPORTED",
            created_documents=0 if force else len(candidates),
            deduplicated_documents=len(candidates) if force else 0,
            provenance_added=len(candidates),
            chunks_created=len(candidates),
            receipt_sha256="1" * 64,
        )


def _write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def _receipt(status: str, imported: int = 0) -> dict[str, object]:
    return {
        "status": status,
        "database_scope": "global-candidates",
        "discovered_count": imported,
        "imported_count": imported,
        "deduplicated_count": 0,
        "provenance_count": imported,
        "chunk_count": imported,
        "excluded_count": 0,
        "not_configured_count": 0,
        "blocked_count": 0,
        "manifest_sha256": None,
        "marker_sha256": "0" * 64,
        "service_receipt_sha256": None,
    }
