"""Compatible schema additions for atomic bootstrap imports."""

from __future__ import annotations

import sqlite3
import uuid


TABLES = (
    """CREATE TABLE import_documents (
        project_id TEXT NOT NULL REFERENCES projects(id),
        content_sha256 TEXT NOT NULL,
        document_id TEXT NOT NULL UNIQUE REFERENCES documents(id),
        version_id INTEGER NOT NULL UNIQUE REFERENCES versions(id),
        PRIMARY KEY(project_id, content_sha256)
    )""",
    """CREATE TABLE import_provenance (
        document_id TEXT NOT NULL REFERENCES documents(id),
        source_kind TEXT NOT NULL,
        source_ref TEXT NOT NULL,
        source_sha256 TEXT NOT NULL,
        scanner TEXT NOT NULL,
        scan_state TEXT NOT NULL CHECK(scan_state = 'CLEAN'),
        governance_state TEXT NOT NULL DEFAULT 'CANDIDATE'
            CHECK(governance_state = 'CANDIDATE'),
        PRIMARY KEY(document_id, source_kind, source_ref)
    )""",
    """CREATE TABLE import_receipts (
        marker TEXT PRIMARY KEY,
        project_id TEXT NOT NULL REFERENCES projects(id),
        receipt TEXT NOT NULL,
        receipt_sha256 TEXT NOT NULL,
        completed_at TEXT NOT NULL
    )""",
)

LEGACY_PROVENANCE = """CREATE TABLE import_provenance (
    document_id TEXT NOT NULL REFERENCES documents(id),
    source_kind TEXT NOT NULL,
    source_ref TEXT NOT NULL,
    source_sha256 TEXT NOT NULL,
    scanner TEXT NOT NULL,
    scan_state TEXT NOT NULL CHECK(scan_state = 'CLEAN'),
    PRIMARY KEY(document_id, source_kind, source_ref)
)"""

DROPS = (
    "DROP TABLE IF EXISTS import_receipts",
    "DROP TABLE IF EXISTS import_provenance",
    "DROP TABLE IF EXISTS import_documents",
)

COLUMNS = {
    "import_documents": (
        ("project_id", "TEXT", 1, None, 1, 0),
        ("content_sha256", "TEXT", 1, None, 2, 0),
        ("document_id", "TEXT", 1, None, 0, 0),
        ("version_id", "INTEGER", 1, None, 0, 0),
    ),
    "import_provenance": (
        ("document_id", "TEXT", 1, None, 1, 0),
        ("source_kind", "TEXT", 1, None, 2, 0),
        ("source_ref", "TEXT", 1, None, 3, 0),
        ("source_sha256", "TEXT", 1, None, 0, 0),
        ("scanner", "TEXT", 1, None, 0, 0),
        ("scan_state", "TEXT", 1, None, 0, 0),
        (
            "governance_state", "TEXT", 1, "'CANDIDATE'", 0, 0,
        ),
    ),
    "import_receipts": (
        ("marker", "TEXT", 0, None, 1, 0),
        ("project_id", "TEXT", 1, None, 0, 0),
        ("receipt", "TEXT", 1, None, 0, 0),
        ("receipt_sha256", "TEXT", 1, None, 0, 0),
        ("completed_at", "TEXT", 1, None, 0, 0),
    ),
}

UNIQUE_KEYS = {
    "import_documents": {
        ("pk", 0, ("project_id", "content_sha256")),
        ("u", 0, ("document_id",)),
        ("u", 0, ("version_id",)),
    },
    "import_provenance": {
        ("pk", 0, ("document_id", "source_kind", "source_ref")),
    },
    "import_receipts": {("pk", 0, ("marker",))},
}

FOREIGN_KEYS = {
    "import_documents": {
        ("project_id", "projects", "id", "NO ACTION", "NO ACTION", "NONE"),
        ("document_id", "documents", "id", "NO ACTION", "NO ACTION", "NONE"),
        ("version_id", "versions", "id", "NO ACTION", "NO ACTION", "NONE"),
    },
    "import_provenance": {
        ("document_id", "documents", "id", "NO ACTION", "NO ACTION", "NONE"),
    },
    "import_receipts": {
        ("project_id", "projects", "id", "NO ACTION", "NO ACTION", "NONE"),
    },
}


def upgrade(connection: sqlite3.Connection) -> None:
    """Create all v3 import tables."""
    for statement in TABLES:
        connection.execute(statement)


def repair(connection: sqlite3.Connection) -> None:
    """Repair missing v3 tables and reject incompatible definitions."""
    for statement, table in zip(TABLES, COLUMNS, strict=True):
        if not _exists(connection, table):
            connection.execute(statement)
        actual = _columns(connection, table)
        expected = COLUMNS[table]
        if table == "import_provenance" and actual == expected[:-1]:
            _validate_sql(connection, table, LEGACY_PROVENANCE)
            _validate_keys(connection, table)
            _rebuild_legacy_provenance(connection)
            actual = _columns(connection, table)
        if actual != expected:
            raise sqlite3.DatabaseError("incompatible schema v3 import table")
        _validate_sql(connection, table, statement)
        _validate_keys(connection, table)
    _validate_rows(connection)
    _probe_provenance_checks(connection)


def _exists(connection: sqlite3.Connection, table: str) -> bool:
    row = connection.execute(
        "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?",
        (table,),
    ).fetchone()
    return row is not None


def _columns(
    connection: sqlite3.Connection, table: str
) -> tuple[tuple[object, ...], ...]:
    rows = connection.execute(f"PRAGMA table_xinfo({table})").fetchall()
    return tuple(
        (
            str(row[1]), str(row[2]).upper(), int(row[3]), row[4],
            int(row[5]), int(row[6]),
        )
        for row in rows
    )


def _validate_sql(
    connection: sqlite3.Connection, table: str, expected: str
) -> None:
    row = connection.execute(
        "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?",
        (table,),
    ).fetchone()
    if row is None or not isinstance(row[0], str):
        raise sqlite3.DatabaseError("schema v3 table SQL is unavailable")
    if _normalized_sql(str(row[0])) != _normalized_sql(expected):
        raise sqlite3.DatabaseError("incompatible schema v3 table SQL")


def _normalized_sql(value: str) -> str:
    return " ".join(value.split()).casefold()


def _rebuild_legacy_provenance(connection: sqlite3.Connection) -> None:
    legacy = "import_provenance_v3_legacy"
    if _exists(connection, legacy):
        raise sqlite3.DatabaseError("legacy schema staging table exists")
    connection.execute(
        f"ALTER TABLE import_provenance RENAME TO {legacy}"
    )
    connection.execute(TABLES[1])
    connection.execute(
        "INSERT INTO import_provenance(document_id, source_kind, source_ref, "
        "source_sha256, scanner, scan_state, governance_state) "
        "SELECT document_id, source_kind, source_ref, source_sha256, scanner, "
        "scan_state, 'CANDIDATE' FROM import_provenance_v3_legacy"
    )
    connection.execute(f"DROP TABLE {legacy}")


def _validate_keys(connection: sqlite3.Connection, table: str) -> None:
    indexes = set()
    for row in connection.execute(f"PRAGMA index_list({table})"):
        if not int(row[2]):
            raise sqlite3.DatabaseError("unexpected schema v3 index")
        index = str(row[1]).replace('"', '""')
        columns = tuple(
            str(item[2])
            for item in connection.execute(
                f'PRAGMA index_info("{index}")'
            )
        )
        indexes.add((str(row[3]), int(row[4]), columns))
    if indexes != UNIQUE_KEYS[table]:
        raise sqlite3.DatabaseError("incompatible schema v3 unique keys")
    foreign = {
        (
            str(row[3]), str(row[2]), str(row[4]), str(row[5]),
            str(row[6]), str(row[7]),
        )
        for row in connection.execute(f"PRAGMA foreign_key_list({table})")
    }
    if foreign != FOREIGN_KEYS[table]:
        raise sqlite3.DatabaseError("incompatible schema v3 foreign keys")
    trigger = connection.execute(
        "SELECT 1 FROM sqlite_master WHERE type = 'trigger' AND tbl_name = ?",
        (table,),
    ).fetchone()
    if trigger is not None:
        raise sqlite3.DatabaseError("unexpected schema v3 trigger")


def _validate_rows(connection: sqlite3.Connection) -> None:
    for table in COLUMNS:
        if connection.execute(
            f"PRAGMA foreign_key_check({table})"
        ).fetchone() is not None:
            raise sqlite3.DatabaseError("orphaned schema v3 import row")


def _probe_provenance_checks(connection: sqlite3.Connection) -> None:
    token = uuid.uuid4().hex
    project_id = f"schema-project-{token}"
    document_id = f"schema-document-{token}"
    source_kind = f"schema-kind-{token}"
    connection.execute("SAVEPOINT import_schema_probe")
    try:
        connection.execute(
            "INSERT INTO projects(id, name, scope, alias, created_at) "
            "VALUES (?, ?, 'global', ?, 'schema-probe')",
            (project_id, project_id, project_id),
        )
        connection.execute(
            "INSERT INTO documents(id, project_id, title) VALUES (?, ?, ?)",
            (document_id, project_id, "schema probe"),
        )
        _insert_probe(
            connection, document_id, source_kind, "valid", "CLEAN",
            "CANDIDATE",
        )
        for value in ("DIRTY", "SAFE"):
            _expect_rejected(
                connection, document_id, source_kind, f"scan-{value}",
                value, "CANDIDATE",
            )
        for value in ("APPROVED", "VERIFIED"):
            _expect_rejected(
                connection, document_id, source_kind, f"state-{value}",
                "CLEAN", value,
            )
    except sqlite3.DatabaseError:
        raise
    except Exception as error:
        raise sqlite3.DatabaseError(
            "schema v3 constraint probe failed"
        ) from error
    finally:
        connection.execute("ROLLBACK TO import_schema_probe")
        connection.execute("RELEASE import_schema_probe")


def _expect_rejected(
    connection: sqlite3.Connection,
    document_id: str,
    source_kind: str,
    source_ref: str,
    scan_state: str,
    governance_state: str,
) -> None:
    try:
        _insert_probe(
            connection, document_id, source_kind, source_ref,
            scan_state, governance_state,
        )
    except sqlite3.IntegrityError:
        return
    raise sqlite3.DatabaseError("schema v3 CHECK constraint is missing")


def _insert_probe(
    connection: sqlite3.Connection,
    document_id: str,
    source_kind: str,
    source_ref: str,
    scan_state: str,
    governance_state: str,
) -> None:
    connection.execute(
        "INSERT INTO import_provenance(document_id, source_kind, source_ref, "
        "source_sha256, scanner, scan_state, governance_state) "
        "VALUES (?, ?, ?, 'sha256', 'scanner', ?, ?)",
        (
            document_id, source_kind, source_ref, scan_state,
            governance_state,
        ),
    )
