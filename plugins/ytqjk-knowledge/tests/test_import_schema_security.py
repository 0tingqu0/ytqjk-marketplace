from __future__ import annotations

import hashlib
import json
import sqlite3
import sys
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.import_migration import (  # noqa: E402
    LEGACY_PROVENANCE,
    TABLES,
)
from scripts.import_receipts import build_receipt  # noqa: E402
from scripts.service import KnowledgeService  # noqa: E402


BROKEN_TABLES = (
    (
        "import_documents",
        TABLES[0].replace(" UNIQUE", ""),
    ),
    (
        "import_provenance",
        TABLES[1].replace("CHECK(scan_state = 'CLEAN')", ""),
    ),
    (
        "import_provenance",
        TABLES[1].replace(
            "CHECK(governance_state = 'CANDIDATE')", ""
        ),
    ),
    (
        "import_provenance",
        TABLES[1].replace(
            "CHECK(scan_state = 'CLEAN')",
            "CHECK(scan_state = 'CLEAN' OR source_kind != 'schema-probe')",
        ),
    ),
    (
        "import_receipts",
        TABLES[2].replace(" REFERENCES projects(id)", ""),
    ),
    (
        "import_receipts",
        TABLES[2].replace(" PRIMARY KEY", ""),
    ),
)


@pytest.mark.parametrize(("table", "definition"), BROKEN_TABLES)
def test_repair_rejects_same_columns_without_required_constraints(
    tmp_path: Path, table: str, definition: str
) -> None:
    database = tmp_path / "knowledge.sqlite3"
    KnowledgeService(database)
    with sqlite3.connect(database) as current:
        current.execute(f"DROP TABLE {table}")
        current.execute(definition)
        current.commit()

    with pytest.raises(sqlite3.DatabaseError, match="schema v3"):
        KnowledgeService(database)


def test_legacy_provenance_is_upgraded_with_candidate_governance(
    tmp_path: Path,
) -> None:
    database = tmp_path / "knowledge.sqlite3"
    KnowledgeService(database)
    with sqlite3.connect(database) as current:
        current.execute(
            "INSERT INTO projects(id, name, scope, alias, created_at) "
            "VALUES ('project', 'project', 'global', 'project', 'now')"
        )
        current.execute(
            "INSERT INTO documents(id, project_id, title) "
            "VALUES ('document', 'project', 'title')"
        )
        current.execute("DROP TABLE import_provenance")
        current.execute(LEGACY_PROVENANCE)
        current.execute(
            "INSERT INTO import_provenance VALUES "
            "('document', 'bootstrap', 'mem.md', 'sha', 'scanner', 'CLEAN')"
        )
        current.commit()

    KnowledgeService(database)

    with sqlite3.connect(database) as current:
        columns = {
            str(row[1])
            for row in current.execute(
                "PRAGMA table_xinfo(import_provenance)"
            )
        }
        state = current.execute(
            "SELECT governance_state FROM import_provenance "
            "WHERE document_id = 'document'"
        ).fetchone()[0]
    assert "governance_state" in columns
    assert state == "CANDIDATE"


def test_repair_rejects_orphaned_import_rows(tmp_path: Path) -> None:
    database = tmp_path / "knowledge.sqlite3"
    KnowledgeService(database)
    with sqlite3.connect(database) as current:
        current.execute(
            "INSERT INTO import_provenance VALUES "
            "('missing', 'bootstrap', 'mem.md', 'sha', 'scanner', "
            "'CLEAN', 'CANDIDATE')"
        )
        current.commit()

    with pytest.raises(sqlite3.DatabaseError, match="orphaned"):
        KnowledgeService(database)


def test_constraint_probe_leaves_no_rows(tmp_path: Path) -> None:
    database = tmp_path / "knowledge.sqlite3"
    KnowledgeService(database)
    KnowledgeService(database)

    with sqlite3.connect(database) as current:
        projects = current.execute(
            "SELECT COUNT(*) FROM projects WHERE alias LIKE 'schema-project-%'"
        ).fetchone()[0]
        provenance = current.execute(
            "SELECT COUNT(*) FROM import_provenance "
            "WHERE source_kind LIKE 'schema-kind-%'"
        ).fetchone()[0]
    assert (projects, provenance) == (0, 0)


@pytest.mark.parametrize(
    "table", ("import_documents", "import_provenance", "import_receipts")
)
def test_canonical_table_with_extra_trigger_is_rejected(
    tmp_path: Path, table: str
) -> None:
    database = tmp_path / "knowledge.sqlite3"
    KnowledgeService(database)
    with sqlite3.connect(database) as current:
        current.execute(
            f"CREATE TRIGGER forged_{table} BEFORE INSERT ON {table} "
            "BEGIN SELECT RAISE(IGNORE); END"
        )
        current.commit()

    with pytest.raises(sqlite3.DatabaseError, match="trigger"):
        KnowledgeService(database)


def test_extra_nonunique_index_is_rejected(tmp_path: Path) -> None:
    database = tmp_path / "knowledge.sqlite3"
    KnowledgeService(database)
    with sqlite3.connect(database) as current:
        current.execute(
            "CREATE INDEX forged_index ON import_provenance(source_sha256)"
        )
        current.commit()

    with pytest.raises(sqlite3.DatabaseError, match="index"):
        KnowledgeService(database)


FORGED_RECEIPTS = (
    {"marker": "other-marker"},
    {"project_id": "other-project"},
    {"created_documents": 999},
    {"input_count": "1"},
    {"unexpected": "field"},
    {"status": "SKIPPED"},
    {"schema_version": 2},
)


@pytest.mark.parametrize("replacement", FORGED_RECEIPTS)
def test_receipt_rejects_rehashed_unbound_or_invalid_payload(
    tmp_path: Path, replacement: dict[str, object]
) -> None:
    service, values = _seed_receipt(tmp_path)
    values.update(replacement)
    _replace_payload(service, values)

    with pytest.raises(RuntimeError, match="integrity check"):
        service.import_receipt("real-marker")


def test_receipt_rejects_row_project_swap(tmp_path: Path) -> None:
    service, _ = _seed_receipt(tmp_path)
    other = service.create_project("global", "other-project")
    with sqlite3.connect(service._database) as current:
        current.execute(
            "UPDATE import_receipts SET project_id = ? "
            "WHERE marker = 'real-marker'",
            (other,),
        )
        current.commit()

    with pytest.raises(RuntimeError, match="integrity check"):
        service.import_receipt("real-marker")


def _seed_receipt(
    tmp_path: Path,
) -> tuple[KnowledgeService, dict[str, object]]:
    service = KnowledgeService(tmp_path / "receipt.sqlite3")
    project_id = service.create_project("global", "codex-bootstrap")
    receipt = build_receipt(
        "real-marker", project_id, 1, [1, 0, 1, 1]
    )
    values = dict(receipt.__dict__)
    digest = str(values.pop("receipt_sha256"))
    payload = _json(values)
    with sqlite3.connect(service._database) as current:
        current.execute(
            "INSERT INTO import_receipts(marker, project_id, receipt, "
            "receipt_sha256, completed_at) VALUES (?, ?, ?, ?, 'now')",
            ("real-marker", project_id, payload, digest),
        )
        current.commit()
    assert service.import_receipt("real-marker") == receipt
    return service, values


def _replace_payload(
    service: KnowledgeService, values: dict[str, object]
) -> None:
    payload = _json(values)
    digest = hashlib.sha256(payload.encode("utf-8")).hexdigest()
    with sqlite3.connect(service._database) as current:
        current.execute(
            "UPDATE import_receipts SET receipt = ?, receipt_sha256 = ? "
            "WHERE marker = 'real-marker'",
            (payload, digest),
        )
        current.commit()


def _json(value: object) -> str:
    return json.dumps(
        value, ensure_ascii=True, sort_keys=True, separators=(",", ":")
    )
