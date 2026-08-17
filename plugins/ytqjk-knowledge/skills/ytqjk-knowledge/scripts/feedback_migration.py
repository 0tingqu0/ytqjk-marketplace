"""Schema v4 migration for governed knowledge feedback."""

from __future__ import annotations

import sqlite3

from .feedback_domain import invalid_version_history
from .feedback_repair import feedback_history_error
from .feedback_schema import COLUMNS as _COLUMNS
from .feedback_schema import FOREIGN_KEYS as _FOREIGN_KEYS
from .feedback_schema import TABLES as _TABLES
from .feedback_schema import UNIQUE_KEYS as _UNIQUE_KEYS


_BASE_KINDS = (
    "'create_project','create_candidate','edit_candidate',"
    "'soft_delete_candidate','append_state','create_snapshot'"
)
_JOB_COLUMNS = (
    ("id", "INTEGER", 0, None, 1, 0),
    ("kind", "TEXT", 1, None, 0, 0),
    ("payload", "TEXT", 1, None, 0, 0),
    ("state", "TEXT", 1, None, 0, 0),
    ("dedupe_key", "TEXT", 0, None, 0, 0),
    ("error", "TEXT", 0, None, 0, 0),
    ("created_at", "TEXT", 1, None, 0, 0),
    ("started_at", "TEXT", 0, None, 0, 0),
    ("finished_at", "TEXT", 0, None, 0, 0),
    ("owner", "TEXT", 0, None, 0, 0),
    ("lease_expires_at", "TEXT", 0, None, 0, 0),
    ("heartbeat_at", "TEXT", 0, None, 0, 0),
    ("attempt", "INTEGER", 1, "0", 0, 0),
)


def _jobs_sql(kinds: str, name: str = "jobs") -> str:
    return f"""CREATE TABLE {name} (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        kind TEXT NOT NULL CHECK(kind IN ({kinds})),
        payload TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN
            ('QUEUED','RUNNING','SUCCEEDED','FAILED')), dedupe_key TEXT UNIQUE,
        error TEXT, created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT,
        owner TEXT, lease_expires_at TEXT, heartbeat_at TEXT,
        attempt INTEGER NOT NULL DEFAULT 0
    )"""


_JOBS_V3 = _jobs_sql(_BASE_KINDS)
_JOBS_V4 = _jobs_sql(f"{_BASE_KINDS},'record_feedback'")
_JOB_TRIGGERS = (
    """CREATE TRIGGER jobs_insert_guard BEFORE INSERT ON jobs WHEN
        NEW.state != 'QUEUED' OR NEW.owner IS NOT NULL
        OR NEW.lease_expires_at IS NOT NULL
        OR NEW.heartbeat_at IS NOT NULL OR NEW.attempt != 0
        BEGIN SELECT RAISE(ABORT, 'jobs must begin queued'); END""",
    """CREATE TRIGGER jobs_payload_immutable BEFORE UPDATE OF kind, payload,
        dedupe_key, created_at ON jobs
        BEGIN SELECT RAISE(ABORT, 'job payload is immutable'); END""",
    """CREATE TRIGGER jobs_state_machine BEFORE UPDATE OF state ON jobs WHEN NOT (
        (OLD.state = 'QUEUED' AND NEW.state = 'RUNNING' AND NEW.owner IS NOT NULL
            AND NEW.lease_expires_at IS NOT NULL AND NEW.heartbeat_at IS NOT NULL
            AND NEW.attempt = OLD.attempt + 1) OR
        (OLD.state = 'RUNNING' AND NEW.state IN ('SUCCEEDED','FAILED')
            AND NEW.owner = OLD.owner AND NEW.attempt = OLD.attempt) OR
        (OLD.state = 'RUNNING' AND NEW.state = 'QUEUED' AND NEW.owner IS NULL
            AND NEW.lease_expires_at IS NULL AND NEW.heartbeat_at IS NULL
            AND NEW.attempt = OLD.attempt
            AND julianday(OLD.lease_expires_at) <= julianday('now')))
        BEGIN SELECT RAISE(ABORT, 'invalid job state transition'); END""",
    """CREATE TRIGGER jobs_lease_guard BEFORE UPDATE OF owner, lease_expires_at,
        heartbeat_at ON jobs WHEN OLD.state = 'RUNNING' AND NEW.state = 'RUNNING'
        AND (NEW.owner != OLD.owner OR NEW.lease_expires_at IS NULL
            OR NEW.heartbeat_at IS NULL)
        BEGIN SELECT RAISE(ABORT, 'invalid job lease update'); END""",
)
_FEEDBACK_TRIGGERS = (
    """CREATE TRIGGER feedback_events_insert_guard BEFORE INSERT ON feedback_events
        WHEN NOT EXISTS (SELECT 1 FROM jobs WHERE id = NEW.job_id
            AND kind = 'record_feedback' AND state = 'RUNNING')
        BEGIN SELECT RAISE(ABORT, 'feedback event requires running job'); END""",
    """CREATE TRIGGER feedback_events_immutable_update BEFORE UPDATE ON feedback_events
        BEGIN SELECT RAISE(ABORT, 'feedback events are append-only'); END""",
    """CREATE TRIGGER feedback_events_immutable_delete BEFORE DELETE ON feedback_events
        BEGIN SELECT RAISE(ABORT, 'feedback events are append-only'); END""",
    """CREATE TRIGGER global_sync_immutable_update BEFORE UPDATE ON global_sync
        BEGIN SELECT RAISE(ABORT, 'global sync links are immutable'); END""",
    """CREATE TRIGGER global_sync_immutable_delete BEFORE DELETE ON global_sync
        BEGIN SELECT RAISE(ABORT, 'global sync links are immutable'); END""",
    """CREATE TRIGGER global_sync_insert_guard BEFORE INSERT ON global_sync WHEN
        NOT EXISTS (SELECT 1 FROM documents source JOIN projects project
            ON project.id = source.project_id WHERE source.id = NEW.source_document_id
            AND project.scope != 'global') OR NOT EXISTS (
            SELECT 1 FROM documents target JOIN projects project
            ON project.id = target.project_id WHERE target.id = NEW.global_document_id
            AND project.scope = 'global' AND project.alias = 'global-knowledge')
        BEGIN SELECT RAISE(ABORT, 'global sync scope is invalid'); END""",
)
_VERSION_TRIGGER = """CREATE TRIGGER versions_state_machine BEFORE INSERT ON versions
    WHEN NEW.ordinal != COALESCE((SELECT MAX(ordinal) + 1 FROM versions
        WHERE document_id = NEW.document_id), 1) OR NOT (
        (NEW.ordinal = 1 AND NEW.state = 'candidate') OR EXISTS (
            SELECT 1 FROM versions prior WHERE prior.document_id = NEW.document_id
            AND prior.ordinal = NEW.ordinal - 1 AND (
                (prior.state = 'candidate' AND NEW.state IN
                    ('candidate','approved','verified','tombstone')) OR
                (prior.state = 'approved' AND NEW.state IN
                    ('candidate','approved','verified','tombstone')) OR
                (prior.state = 'verified' AND NEW.state IN
                    ('approved','verified','tombstone')))))
    BEGIN SELECT RAISE(ABORT, 'invalid governance state transition'); END"""


def upgrade(connection: sqlite3.Connection) -> None:
    """Add feedback governance tables and queue operation."""
    _validate_jobs(connection, _JOBS_V3)
    _rebuild_jobs(connection, allow_feedback=True)
    for statement in _TABLES.values():
        connection.execute(statement)
    connection.execute("DROP TRIGGER IF EXISTS versions_state_machine")
    connection.execute(_VERSION_TRIGGER)
    _repair_triggers(connection, _FEEDBACK_TRIGGERS, exact_by_table=True)


def downgrade(connection: sqlite3.Connection) -> None:
    """Remove an unused feedback schema, failing closed when it has data."""
    if connection.execute(
        "SELECT 1 FROM jobs WHERE kind = 'record_feedback' LIMIT 1"
    ).fetchone():
        raise sqlite3.DatabaseError("schema v4 feedback jobs prevent downgrade")
    if any(
        connection.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
        for table in _TABLES
    ):
        raise sqlite3.DatabaseError("schema v4 feedback data prevents downgrade")
    if invalid_version_history(connection, allow_feedback=False):
        raise sqlite3.DatabaseError("schema v4 version history prevents downgrade")
    _drop_triggers(connection, _FEEDBACK_TRIGGERS + (_VERSION_TRIGGER,))
    connection.execute("DROP TABLE global_sync")
    connection.execute("DROP TABLE feedback_events")
    _rebuild_jobs(connection, allow_feedback=False)


def repair(connection: sqlite3.Connection) -> None:
    """Restore missing guards and reject incompatible schema or rows."""
    sql = _table_sql(connection, "jobs")
    if _normalized(sql) == _normalized(_JOBS_V3):
        _validate_jobs(connection, _JOBS_V3)
        _rebuild_jobs(connection, allow_feedback=True)
    else:
        _validate_jobs(connection, _JOBS_V4)
    for table, statement in _TABLES.items():
        if _table_sql(connection, table) is None:
            raise sqlite3.DatabaseError(f"missing schema v4 {table} table")
        _validate_table(connection, table, statement, _COLUMNS[table])
    _repair_triggers(connection, _JOB_TRIGGERS, exact_by_table=True)
    _repair_triggers(connection, _FEEDBACK_TRIGGERS, exact_by_table=True)
    _repair_triggers(connection, (_VERSION_TRIGGER,), exact_by_table=False)
    _validate_rows(connection)


def _rebuild_jobs(connection: sqlite3.Connection, *, allow_feedback: bool) -> None:
    if _table_sql(connection, "jobs_next") is not None:
        raise sqlite3.DatabaseError("schema v4 jobs staging table exists")
    _drop_triggers(connection, _JOB_TRIGGERS)
    kinds = f"{_BASE_KINDS},'record_feedback'" if allow_feedback else _BASE_KINDS
    connection.execute(_jobs_sql(kinds, "jobs_next"))
    columns = ",".join(item[0] for item in _JOB_COLUMNS)
    where = "" if allow_feedback else " WHERE kind != 'record_feedback'"
    connection.execute(
        f"INSERT INTO jobs_next({columns}) SELECT {columns} FROM jobs{where}"
    )
    connection.execute("DROP TABLE jobs")
    connection.execute("ALTER TABLE jobs_next RENAME TO jobs")
    for statement in _JOB_TRIGGERS:
        connection.execute(statement)


def _validate_jobs(connection: sqlite3.Connection, expected_sql: str) -> None:
    _validate_table(connection, "jobs", expected_sql, _JOB_COLUMNS)
    _repair_triggers(connection, _JOB_TRIGGERS, exact_by_table=True)


def _validate_table(
    connection: sqlite3.Connection,
    table: str,
    expected_sql: str,
    expected_columns: tuple[tuple[object, ...], ...],
) -> None:
    if _normalized(_table_sql(connection, table)) != _normalized(expected_sql):
        raise sqlite3.DatabaseError(f"incompatible schema v4 {table} SQL")
    columns = tuple(
        (
            str(row[1]), str(row[2]).upper(), int(row[3]),
            row[4], int(row[5]), int(row[6]),
        )
        for row in connection.execute(f"PRAGMA table_xinfo({table})")
    )
    if columns != expected_columns:
        raise sqlite3.DatabaseError(f"incompatible schema v4 {table} columns")
    indexes = set()
    for row in connection.execute(f"PRAGMA index_list({table})"):
        index = str(row[1]).replace('"', '""')
        names = tuple(
            str(item[2]) for item in connection.execute(f'PRAGMA index_info("{index}")')
        )
        indexes.add((str(row[3]), int(row[4]), names))
    if indexes != _UNIQUE_KEYS[table]:
        raise sqlite3.DatabaseError(f"incompatible schema v4 {table} unique keys")
    foreign = {
        (str(row[3]), str(row[2]), str(row[4]), str(row[5]), str(row[6]), str(row[7]))
        for row in connection.execute(f"PRAGMA foreign_key_list({table})")
    }
    if foreign != _FOREIGN_KEYS[table]:
        raise sqlite3.DatabaseError(f"incompatible schema v4 {table} foreign keys")


def _repair_triggers(
    connection: sqlite3.Connection,
    expected: tuple[str, ...],
    *,
    exact_by_table: bool,
) -> None:
    definitions = {_trigger_name(item): item for item in expected}
    tables = {_trigger_table(item) for item in expected}
    rows = connection.execute(
        "SELECT name, tbl_name, sql FROM sqlite_master WHERE type = 'trigger'"
    ).fetchall()
    actual = {str(row[0]): str(row[2]) for row in rows if str(row[1]) in tables}
    if exact_by_table and set(actual) - set(definitions):
        raise sqlite3.DatabaseError("unexpected schema v4 trigger")
    for name, statement in definitions.items():
        if name not in actual:
            connection.execute(statement)
        elif _normalized(actual[name]) != _normalized(statement):
            raise sqlite3.DatabaseError("incompatible schema v4 trigger")


def _validate_rows(connection: sqlite3.Connection) -> None:
    for table in _TABLES:
        if connection.execute(f"PRAGMA foreign_key_check({table})").fetchone():
            raise sqlite3.DatabaseError("orphaned schema v4 row")
    feedback_error = feedback_history_error(connection)
    if feedback_error:
        raise sqlite3.DatabaseError(f"invalid schema v4 {feedback_error}")
    invalid_link = connection.execute(
        "SELECT 1 FROM global_sync link JOIN documents source ON source.id = "
        "link.source_document_id JOIN projects source_project ON source_project.id = "
        "source.project_id JOIN documents target ON target.id = "
        "link.global_document_id "
        "JOIN projects target_project ON target_project.id = target.project_id WHERE "
        "source_project.scope = 'global' OR target_project.scope != 'global' OR "
        "target_project.alias != 'global-knowledge' LIMIT 1"
    ).fetchone()
    if invalid_link:
        raise sqlite3.DatabaseError("invalid schema v4 global sync scope")
    if invalid_version_history(connection):
        raise sqlite3.DatabaseError("invalid schema v4 version history")


def _drop_triggers(
    connection: sqlite3.Connection, statements: tuple[str, ...]
) -> None:
    for statement in statements:
        connection.execute(f"DROP TRIGGER IF EXISTS {_trigger_name(statement)}")


def _trigger_name(statement: str) -> str:
    return statement.split()[2]


def _trigger_table(statement: str) -> str:
    words = statement.split()
    return words[words.index("ON") + 1]


def _table_sql(connection: sqlite3.Connection, table: str) -> str | None:
    row = connection.execute(
        "SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?", (table,)
    ).fetchone()
    return str(row[0]) if row and row[0] is not None else None


def _normalized(value: str | None) -> str:
    compact = " ".join((value or "").replace('"', "").split())
    return compact.replace(", ", ",").casefold()
