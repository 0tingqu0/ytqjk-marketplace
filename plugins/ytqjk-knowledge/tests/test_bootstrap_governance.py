from __future__ import annotations

import sqlite3
import sys
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.import_contracts import CandidateImport  # noqa: E402
from scripts.intake_parsers import default_registry  # noqa: E402
from scripts.intake_security import inspect_input  # noqa: E402
from scripts.service import KnowledgeService  # noqa: E402


@pytest.fixture
def service(tmp_path: Path) -> KnowledgeService:
    return KnowledgeService(tmp_path / "target" / "knowledge.sqlite3")


def candidate(root: Path, name: str, content: str) -> CandidateImport:
    path = root / name
    path.write_text(content, encoding="utf-8")
    source = inspect_input(root, path)[0]
    parsed = default_registry().parse(source)
    return CandidateImport(path.stem, parsed)


def test_approved_dedupe_keeps_new_provenance_candidate_only(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    first = candidate(root, "first.md", "governed content")
    receipt = service.import_candidates(
        "global", "codex-bootstrap", "initial", (first,)
    )
    document_id = str(_one(service, "SELECT id FROM documents")[0])
    service.append_state(document_id, "approved")
    sources_before = service.count("sources")
    second = candidate(root, "second.md", "governed content")

    result = service.import_candidates(
        "global", "codex-bootstrap", "later", (second,)
    )

    assert result.project_id == receipt.project_id
    assert result.created_documents == 0
    assert result.deduplicated_documents == 1
    assert result.provenance_added == 1
    assert service.count("documents") == 1
    assert service.count("import_provenance") == 2
    assert service.count("sources") == sources_before
    states = service.document_versions(document_id)
    assert [row["state"] for row in states] == ["candidate", "approved"]
    provenance = _rows(
        service,
        "SELECT governance_state FROM import_provenance ORDER BY source_ref",
    )
    assert provenance == [("CANDIDATE",), ("CANDIDATE",)]


def test_same_source_refreshes_changed_raw_digest_idempotently(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    first = candidate(root, "same.json", '{"value":1}')
    service.import_candidates(
        "global", "codex-bootstrap", "refresh", (first,)
    )
    second = candidate(root, "same.json", '{\n  "value": 1\n}')
    assert first.parsed.content_sha256 == second.parsed.content_sha256

    refreshed = service.import_candidates(
        "global", "codex-bootstrap", "refresh", (second,), force=True
    )
    repeated = service.import_candidates(
        "global", "codex-bootstrap", "refresh", (second,), force=True
    )

    assert refreshed.provenance_added == 1
    assert repeated.provenance_added == 0
    assert service.count("import_provenance") == 1
    stored = _one(
        service, "SELECT source_sha256 FROM import_provenance"
    )[0]
    assert stored == second.parsed.source.sha256


def _rows(
    service: KnowledgeService, statement: str
) -> list[tuple[object, ...]]:
    with sqlite3.connect(service._database) as current:
        return current.execute(statement).fetchall()


def _one(service: KnowledgeService, statement: str) -> tuple[object, ...]:
    return _rows(service, statement)[0]
