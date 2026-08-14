from __future__ import annotations

import hashlib
import json
import sqlite3
import sys
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.import_contracts import CandidateImport  # noqa: E402
from scripts.intake_contracts import ScanState  # noqa: E402
from scripts.intake_parsers import default_registry  # noqa: E402
from scripts.intake_security import inspect_input  # noqa: E402
from scripts.migrations import migrate  # noqa: E402
from scripts.service import KnowledgeService  # noqa: E402


def candidate(
    root: Path,
    name: str,
    content: str,
    *,
    chunk_chars: int = 5,
) -> CandidateImport:
    path = root / name
    path.write_text(content, encoding="utf-8")
    source = inspect_input(root, path)[0]
    parsed = default_registry(chunk_chars=chunk_chars).parse(source)
    return CandidateImport(path.stem, parsed)


@pytest.fixture
def service(tmp_path: Path) -> KnowledgeService:
    return KnowledgeService(tmp_path / "target" / "knowledge.sqlite3")


def test_import_deduplicates_content_and_persists_real_chunks(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    items = (
        candidate(root, "first.md", "abcdefghijk"),
        candidate(root, "second.md", "abcdefghijk"),
    )

    receipt = service.import_candidates(
        "global", "codex-bootstrap", "first-install", items
    )

    assert receipt.status == "IMPORTED"
    assert receipt.created_documents == 1
    assert receipt.deduplicated_documents == 1
    assert receipt.provenance_added == 2
    assert receipt.chunks_created == 3
    assert service.count("documents") == 1
    assert service.count("versions") == 1
    assert service.count("chunks") == 3
    assert service.count("import_provenance") == 2
    document_id = _one(service, "SELECT id FROM documents")[0]
    versions = service.document_versions(str(document_id))
    assert versions[0]["state"] == "candidate"


def test_marker_skips_and_force_only_adds_missing_provenance(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    first = candidate(root, "first.md", "same content")
    service.import_candidates(
        "global", "codex-bootstrap", "first-install", (first,)
    )
    second = candidate(root, "second.md", "same content")

    skipped = service.import_candidates(
        "global", "codex-bootstrap", "first-install", (second,)
    )
    forced = service.import_candidates(
        "global",
        "codex-bootstrap",
        "first-install",
        (second,),
        force=True,
    )

    assert skipped.status == "SKIPPED"
    values = dict(skipped.__dict__)
    digest = values.pop("receipt_sha256")
    payload = json.dumps(
        values, ensure_ascii=True, sort_keys=True, separators=(",", ":")
    )
    assert digest == hashlib.sha256(payload.encode("utf-8")).hexdigest()
    assert forced.status == "IMPORTED"
    assert forced.created_documents == 0
    assert forced.deduplicated_documents == 1
    assert forced.provenance_added == 1
    assert service.count("documents") == 1
    assert service.count("import_provenance") == 2
    assert service.import_receipt("first-install") == forced


def test_project_candidates_and_marker_roll_back_together(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    items = (
        candidate(root, "first.md", "first content"),
        candidate(root, "second.md", "second content"),
    )
    with sqlite3.connect(service._database) as current:
        current.execute(
            "CREATE TRIGGER fail_second BEFORE INSERT ON import_provenance "
            "WHEN NEW.source_ref = 'second.md' BEGIN "
            "SELECT RAISE(ABORT, 'injected failure'); END"
        )

    with pytest.raises(sqlite3.IntegrityError, match="injected failure"):
        service.import_candidates(
            "global", "rollback-bootstrap", "rollback-marker", items
        )

    assert service.count("documents") == 0
    assert service.count("originals") == 0
    assert service.count("import_receipts") == 0
    assert _rows(
        service,
        "SELECT id FROM projects WHERE alias = 'rollback-bootstrap'",
    ) == []


def test_schema_v3_upgrade_failure_is_atomic_and_downgrade_is_scoped(
    tmp_path: Path,
) -> None:
    database = tmp_path / "migration.sqlite3"
    with sqlite3.connect(database) as current:
        migrate(current, 2)
        current.execute("CREATE TABLE import_documents (broken TEXT)")
        current.commit()
        with pytest.raises(sqlite3.OperationalError, match="already exists"):
            migrate(current, 3)
        assert current.execute("PRAGMA user_version").fetchone()[0] == 2
        tables = current.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table' "
            "AND name LIKE 'import_%' ORDER BY name"
        ).fetchall()
        assert tables == [("import_documents",)]
        current.execute("DROP TABLE import_documents")
        migrate(current, 3)
        assert current.execute("PRAGMA user_version").fetchone()[0] == 3
        migrate(current, 2)
        assert current.execute("PRAGMA user_version").fetchone()[0] == 2
        tables = current.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table' "
            "AND name LIKE 'import_%'"
        ).fetchall()
        assert tables == []
        snapshots = current.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table' "
            "AND name = 'snapshots'"
        ).fetchall()
        assert snapshots == [("snapshots",)]


def test_concurrent_marker_import_is_idempotent(tmp_path: Path) -> None:
    database = tmp_path / "target" / "knowledge.sqlite3"
    root = tmp_path / "input"
    root.mkdir()
    item = candidate(root, "first.md", "concurrent content")
    services = (KnowledgeService(database), KnowledgeService(database))

    def run(current: KnowledgeService) -> str:
        receipt = current.import_candidates(
            "global", "codex-bootstrap", "first-install", (item,)
        )
        return receipt.status

    with ThreadPoolExecutor(max_workers=2) as pool:
        statuses = list(pool.map(run, services))

    assert sorted(statuses) == ["IMPORTED", "SKIPPED"]
    assert services[0].count("documents") == 1
    assert services[0].count("import_receipts") == 1


def test_marker_cannot_be_rebound_even_with_force(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    item = candidate(root, "first.md", "bound marker")
    original = service.import_candidates(
        "global", "codex-bootstrap", "first-install", (item,)
    )

    with pytest.raises(ValueError, match="belongs to another project"):
        service.import_candidates(
            "global",
            "other-bootstrap",
            "first-install",
            (item,),
            force=True,
        )

    assert service.import_receipt("first-install") == original
    assert _rows(
        service, "SELECT id FROM projects WHERE alias = 'other-bootstrap'"
    ) == []


def test_extensionless_text_requires_opt_in_and_rejects_binary(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    memory = root / "memory-entry"
    memory.write_text("extensionless memory", encoding="utf-8")
    source = inspect_input(root, memory)[0]
    with pytest.raises(ValueError, match="extension is required"):
        default_registry().parse(source)
    parsed = default_registry(
        chunk_chars=6,
        allow_extensionless_text=True,
    ).parse(source)

    receipt = service.import_candidates(
        "global",
        "codex-bootstrap",
        "extensionless",
        (CandidateImport("memory-entry", parsed),),
    )

    assert receipt.created_documents == 1
    assert service.count("chunks") == 4
    binary = root / "binary-entry"
    binary.write_bytes(b"plain-prefix\0binary")
    inspected = inspect_input(root, binary)[0]
    with pytest.raises(ValueError, match="not plain text"):
        default_registry(allow_extensionless_text=True).parse(inspected)


def test_public_boundary_rejects_governance_and_scan_forgery(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    valid = candidate(root, "first.md", "candidate only")
    promoted = replace(valid, governance_state="APPROVED")
    forged_scan = replace(
        valid.parsed.output_scan,
        state=ScanState.BLOCKED,
    )
    parsed = replace(valid.parsed, output_scan=forged_scan)
    forged = replace(valid, parsed=parsed)

    with pytest.raises(ValueError, match="must be CANDIDATE"):
        service.import_candidates(
            "global", "codex-bootstrap", "governance", (promoted,)
        )
    with pytest.raises(ValueError, match="scanner proof"):
        service.import_candidates(
            "global", "codex-bootstrap", "scan-proof", (forged,)
        )
    assert service.count("documents") == 0
    assert service.count("import_receipts") == 0


def test_receipt_excludes_source_path_username_and_content(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    item = candidate(root, "private-note.md", "do not echo this")
    receipt = service.import_candidates(
        "global", "codex-bootstrap", "safe-marker", (item,)
    )
    payload = _one(
        service,
        "SELECT receipt FROM import_receipts WHERE marker = 'safe-marker'",
    )[0]
    serialized = json.dumps(receipt.__dict__) + str(payload)

    assert str(root) not in serialized
    assert "private-note.md" not in serialized
    assert "do not echo this" not in serialized
    assert Path.home().name not in serialized


def test_dedupe_uses_only_current_visible_content(
    service: KnowledgeService, tmp_path: Path
) -> None:
    root = tmp_path / "input"
    root.mkdir()
    original = candidate(root, "first.md", "original content")
    service.import_candidates(
        "global", "codex-bootstrap", "initial", (original,)
    )
    document_id = str(_one(service, "SELECT id FROM documents")[0])
    service.edit_candidate(document_id, "edited away", "test")

    second = service.import_candidates(
        "global", "codex-bootstrap", "reimport", (original,)
    )

    assert second.created_documents == 1
    assert service.count("documents") == 2


def _rows(
    service: KnowledgeService, statement: str
) -> list[tuple[object, ...]]:
    with sqlite3.connect(service._database) as current:
        return current.execute(statement).fetchall()


def _one(service: KnowledgeService, statement: str) -> tuple[object, ...]:
    return _rows(service, statement)[0]
