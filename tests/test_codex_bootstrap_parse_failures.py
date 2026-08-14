from __future__ import annotations

import io
import json
from contextlib import redirect_stdout
from pathlib import Path
from types import SimpleNamespace

from codex_bootstrap_import import Dependencies, import_codex_candidates
from setup import main


class _FakeService:
    def __init__(self) -> None:
        self.candidates: tuple[object, ...] | None = None

    def import_receipt(self, marker: str) -> None:
        del marker
        return None

    def import_candidates(
        self,
        scope: str,
        alias: str,
        marker: str,
        candidates: tuple[object, ...],
        *,
        force: bool,
    ) -> object:
        del scope, alias, marker, force
        self.candidates = candidates
        return SimpleNamespace(
            status="IMPORTED",
            created_documents=len(candidates),
            deduplicated_documents=0,
            provenance_added=len(candidates),
            chunks_created=len(candidates),
            receipt_sha256="1" * 64,
        )


def _dependencies(root: Path, service: _FakeService) -> Dependencies:
    def inspect(source_root: Path, path: Path) -> tuple[object, ...]:
        relative = path.relative_to(source_root).as_posix()
        return (SimpleNamespace(relative_path=relative, sha256="a" * 64),)

    def parse(source: object) -> object:
        if source.relative_path.endswith("broken.json"):
            raise ValueError("invalid historical file")
        return SimpleNamespace(source=source)

    return Dependencies(
        inspect,
        lambda **kwargs: SimpleNamespace(parse=parse),
        lambda database: service,
        lambda **values: SimpleNamespace(**values),
    )


def _source_with_one_bad_file(root: Path) -> Path:
    source = root / "codex"
    memories = source / "memories"
    memories.mkdir(parents=True)
    (memories / "valid.md").write_text("valid", encoding="utf-8")
    (memories / "broken.json").write_text("{", encoding="utf-8")
    return source


def test_auto_import_isolates_parse_failure_and_imports_valid_files(
    tmp_path: Path,
) -> None:
    source = _source_with_one_bad_file(tmp_path)
    service = _FakeService()

    receipt = import_codex_candidates(
        source,
        tmp_path / "knowledge",
        "auto",
        dependencies=_dependencies(source, service),
    )

    assert receipt["status"] == "SUCCEEDED_WITH_WARNINGS"
    assert receipt["failure_stage"] == "PARSING"
    assert receipt["failure_code"] == "PARSE_FAILED"
    assert receipt["discovered_count"] == 2
    assert receipt["parse_failed_count"] == 1
    assert receipt["imported_count"] == 1
    assert service.candidates is not None
    assert len(service.candidates) == 1


def test_force_import_keeps_parse_failures_strict(tmp_path: Path) -> None:
    source = _source_with_one_bad_file(tmp_path)
    service = _FakeService()

    receipt = import_codex_candidates(
        source,
        tmp_path / "knowledge",
        "force",
        dependencies=_dependencies(source, service),
    )

    assert receipt["status"] == "FAILED"
    assert receipt["failure_code"] == "PARSE_FAILED"
    assert receipt["parse_failed_count"] == 1
    assert service.candidates is None


def test_auto_parse_warning_does_not_fail_complete_install(
    tmp_path: Path,
) -> None:
    source = _source_with_one_bad_file(tmp_path)
    service = _FakeService()
    import_receipt = import_codex_candidates(
        source,
        tmp_path / "knowledge",
        "auto",
        dependencies=_dependencies(source, service),
    )
    output = io.StringIO()

    with redirect_stdout(output):
        code = main(
            [
                "--apply",
                "--yes",
                "--mode",
                "knowledge-only",
                "--target-root",
                str(tmp_path / "target"),
                "--project-bootstrap",
                "off",
                "--json",
            ],
            runner=lambda command, cwd: "",
            codex_importer=lambda codex, knowledge, mode: import_receipt,
        )

    receipt = json.loads(output.getvalue())
    assert code == 0
    assert receipt["apply"]["status"] == "APPLIED"
    assert receipt["knowledge_import"]["status"] == (
        "SUCCEEDED_WITH_WARNINGS"
    )
