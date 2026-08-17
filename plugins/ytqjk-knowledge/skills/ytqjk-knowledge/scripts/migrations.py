"""Transactional v1-v4 SQLite migrations for YTQJK Knowledge."""

from __future__ import annotations

import sqlite3
from datetime import datetime, timezone

from .feedback_migration import downgrade as _downgrade_feedback
from .feedback_migration import repair as _repair_feedback
from .feedback_migration import upgrade as _upgrade_feedback
from .import_migration import DROPS as _IMPORT_DROPS
from .import_migration import repair as _repair_import
from .import_migration import upgrade as _upgrade_import


LATEST_VERSION = 4


def migrate(connection: sqlite3.Connection, target: int = LATEST_VERSION) -> None:
    """Lock, read version, migrate, and repair idempotently in one transaction."""
    if target not in (1, 2, 3, 4):
        raise ValueError("unsupported schema version")
    connection.execute("BEGIN IMMEDIATE")
    try:
        current = int(connection.execute("PRAGMA user_version").fetchone()[0])
        while current < target:
            _upgrade(connection, current + 1)
            current += 1
        while current > target:
            _downgrade(connection, current)
            current -= 1
        if current >= 1:
            _repair_v1(connection, preserve_feedback=current >= 4)
        if current >= 2:
            _execute(connection, _SNAPSHOT_TRIGGER_DROPS + _SNAPSHOT_TRIGGERS)
        if current >= 3:
            _repair_import(connection)
        if current >= 4:
            _repair_feedback(connection)
        connection.execute(f"PRAGMA user_version = {current}")
        connection.commit()
    except BaseException:
        connection.rollback()
        raise


def _upgrade(connection: sqlite3.Connection, version: int) -> None:
    if version == 1:
        _execute(connection, _V1_TABLES)
    elif version == 2:
        _execute(connection, _V2_TABLES)
    elif version == 3:
        _upgrade_import(connection)
    elif version == 4:
        _upgrade_feedback(connection)
    else:
        raise ValueError("unsupported schema version")


def _downgrade(connection: sqlite3.Connection, version: int) -> None:
    if version == 4:
        _downgrade_feedback(connection)
        return
    if version == 3:
        _execute(connection, _IMPORT_DROPS)
        return
    if version != 2:
        raise ValueError("unsupported schema version")
    _execute(connection, _SNAPSHOT_TRIGGER_DROPS + _V2_TABLE_DROPS)


def _repair_v1(
    connection: sqlite3.Connection, *, preserve_feedback: bool = False
) -> None:
    if connection.execute(
        "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'jobs'"
    ).fetchone() is None:
        connection.execute(_JOBS_TABLE)
    columns = {
        str(row[1]) for row in connection.execute("PRAGMA table_info(jobs)")
    }
    for name, definition in _JOB_COLUMNS:
        if name not in columns:
            connection.execute(f"ALTER TABLE jobs ADD COLUMN {name} {definition}")
    _repair_running_jobs(connection)
    drops = _V1_TRIGGER_DROPS
    triggers = _V1_TRIGGERS
    if preserve_feedback:
        protected = "versions_state_machine"
        drops = tuple(item for item in drops if protected not in item)
        triggers = tuple(item for item in triggers if protected not in item)
    _execute(connection, drops + _JOB_TRIGGER_DROPS + triggers + _JOB_TRIGGERS)


def _repair_running_jobs(connection: sqlite3.Connection) -> None:
    reference = datetime.now(timezone.utc)
    recoverable: list[tuple[int]] = []
    live_count = 0
    for job_id, owner, heartbeat, raw_lease in connection.execute(
        "SELECT id, owner, heartbeat_at, lease_expires_at FROM jobs "
        "WHERE state = 'RUNNING' ORDER BY id"
    ):
        if owner is None or heartbeat is None or raw_lease is None:
            recoverable.append((int(job_id),))
            continue
        try:
            lease = datetime.fromisoformat(raw_lease)
        except (TypeError, ValueError) as error:
            raise sqlite3.DatabaseError("invalid RUNNING job lease") from error
        if lease.tzinfo is None:
            raise sqlite3.DatabaseError("invalid RUNNING job lease")
        if lease.astimezone(timezone.utc) <= reference:
            recoverable.append((int(job_id),))
        else:
            live_count += 1
    if live_count > 1:
        raise sqlite3.DatabaseError("multiple live RUNNING job leases")
    _execute(connection, _JOB_TRIGGER_DROPS)
    connection.executemany(
        "UPDATE jobs SET state = 'QUEUED', owner = NULL, heartbeat_at = NULL, "
        "lease_expires_at = NULL WHERE id = ?", recoverable,
    )


def _execute(connection: sqlite3.Connection, statements: tuple[str, ...]) -> None:
    for statement in statements:
        connection.execute(statement)


_V1_TABLES = (
    """CREATE TABLE projects (
        id TEXT PRIMARY KEY, name TEXT NOT NULL, scope TEXT NOT NULL,
        alias TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL
    )""",
    """CREATE TABLE originals (
        sha256 TEXT PRIMARY KEY, content BLOB NOT NULL, created_at TEXT NOT NULL
    )""",
    """CREATE TABLE documents (
        id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
        title TEXT NOT NULL, deleted_at TEXT
    )""",
    """CREATE TABLE versions (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        document_id TEXT NOT NULL REFERENCES documents(id), ordinal INTEGER NOT NULL,
        state TEXT NOT NULL CHECK(state IN ('candidate','approved','verified','tombstone')),
        original_sha256 TEXT NOT NULL REFERENCES originals(sha256),
        created_at TEXT NOT NULL, UNIQUE(document_id, ordinal)
    )""",
    """CREATE TABLE chunks (
        id TEXT PRIMARY KEY, version_id INTEGER NOT NULL REFERENCES versions(id),
        ordinal INTEGER NOT NULL, content TEXT NOT NULL, UNIQUE(version_id, ordinal)
    )""",
    """CREATE TABLE sources (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        version_id INTEGER NOT NULL REFERENCES versions(id), kind TEXT NOT NULL,
        locator TEXT NOT NULL
    )""",
    """CREATE TABLE governance (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        version_id INTEGER NOT NULL REFERENCES versions(id), action TEXT NOT NULL,
        actor TEXT NOT NULL, created_at TEXT NOT NULL
    )""",
    """CREATE TABLE audit (
        id INTEGER PRIMARY KEY AUTOINCREMENT, event TEXT NOT NULL,
        subject_id TEXT NOT NULL, created_at TEXT NOT NULL, detail TEXT NOT NULL
    )""",
    """CREATE TABLE jobs (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        kind TEXT NOT NULL CHECK(kind IN ('create_project','create_candidate',
            'edit_candidate','soft_delete_candidate','append_state','create_snapshot')),
        payload TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN
            ('QUEUED','RUNNING','SUCCEEDED','FAILED')), dedupe_key TEXT UNIQUE,
        error TEXT, created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT,
        owner TEXT, lease_expires_at TEXT, heartbeat_at TEXT,
        attempt INTEGER NOT NULL DEFAULT 0
    )""",
)

_V2_TABLES = (
    """CREATE TABLE snapshots (
        id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
        generation INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('BUILDING','ACTIVE')),
        created_at TEXT NOT NULL, UNIQUE(project_id, generation)
    )""",
    """CREATE TABLE snapshot_versions (
        snapshot_id TEXT NOT NULL REFERENCES snapshots(id),
        document_id TEXT NOT NULL REFERENCES documents(id),
        version_id INTEGER NOT NULL REFERENCES versions(id),
        PRIMARY KEY(snapshot_id, document_id)
    )""",
    """CREATE TABLE active_snapshots (
        project_id TEXT PRIMARY KEY REFERENCES projects(id),
        snapshot_id TEXT NOT NULL REFERENCES snapshots(id)
    )""",
)

_JOBS_TABLE = _V1_TABLES[-1]

_JOB_COLUMNS = (
    ("owner", "TEXT"),
    ("lease_expires_at", "TEXT"),
    ("heartbeat_at", "TEXT"),
    ("attempt", "INTEGER NOT NULL DEFAULT 0"),
)

_V1_TRIGGER_DROPS = (
    "DROP TRIGGER IF EXISTS projects_immutable",
    "DROP TRIGGER IF EXISTS documents_soft_delete_candidate",
    "DROP TRIGGER IF EXISTS originals_immutable_update",
    "DROP TRIGGER IF EXISTS originals_immutable_delete",
    "DROP TRIGGER IF EXISTS versions_append_only",
    "DROP TRIGGER IF EXISTS versions_no_delete",
    "DROP TRIGGER IF EXISTS versions_state_machine",
    "DROP TRIGGER IF EXISTS audit_immutable_update",
    "DROP TRIGGER IF EXISTS audit_immutable_delete",
)

_V1_TRIGGERS = (
    """CREATE TRIGGER projects_immutable BEFORE UPDATE OF id, scope, alias ON projects
        BEGIN SELECT RAISE(ABORT, 'project identity is immutable'); END""",
    """CREATE TRIGGER documents_soft_delete_candidate BEFORE UPDATE OF deleted_at ON documents
        WHEN NOT (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND EXISTS (
            SELECT 1 FROM versions WHERE document_id = OLD.id AND ordinal = (
                SELECT MAX(ordinal) FROM versions WHERE document_id = OLD.id)
            AND state = 'candidate'))
        BEGIN SELECT RAISE(ABORT, 'only active candidates can be soft deleted'); END""",
    """CREATE TRIGGER originals_immutable_update BEFORE UPDATE ON originals
        BEGIN SELECT RAISE(ABORT, 'originals are immutable'); END""",
    """CREATE TRIGGER originals_immutable_delete BEFORE DELETE ON originals
        BEGIN SELECT RAISE(ABORT, 'originals are immutable'); END""",
    """CREATE TRIGGER versions_append_only BEFORE UPDATE ON versions
        BEGIN SELECT RAISE(ABORT, 'versions are append-only'); END""",
    """CREATE TRIGGER versions_no_delete BEFORE DELETE ON versions
        BEGIN SELECT RAISE(ABORT, 'versions are append-only'); END""",
    """CREATE TRIGGER versions_state_machine BEFORE INSERT ON versions
        WHEN NEW.ordinal != COALESCE((SELECT MAX(ordinal) + 1 FROM versions
            WHERE document_id = NEW.document_id), 1) OR NOT (
            (NEW.ordinal = 1 AND NEW.state = 'candidate') OR EXISTS (
                SELECT 1 FROM versions prior WHERE prior.document_id = NEW.document_id
                AND prior.ordinal = NEW.ordinal - 1 AND (
                    (prior.state = 'candidate' AND NEW.state IN
                        ('candidate','approved','verified','tombstone')) OR
                    (prior.state = 'approved' AND NEW.state IN ('verified','tombstone')) OR
                    (prior.state = 'verified' AND NEW.state = 'tombstone'))))
        BEGIN SELECT RAISE(ABORT, 'invalid governance state transition'); END""",
    """CREATE TRIGGER audit_immutable_update BEFORE UPDATE ON audit
        BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END""",
    """CREATE TRIGGER audit_immutable_delete BEFORE DELETE ON audit
        BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END""",
)

_JOB_TRIGGER_DROPS = (
    "DROP TRIGGER IF EXISTS jobs_insert_guard",
    "DROP TRIGGER IF EXISTS jobs_payload_immutable",
    "DROP TRIGGER IF EXISTS jobs_state_machine",
    "DROP TRIGGER IF EXISTS jobs_lease_guard",
)

_SNAPSHOT_TRIGGER_DROPS = (
    "DROP TRIGGER IF EXISTS snapshots_insert_guard",
    "DROP TRIGGER IF EXISTS snapshots_immutable",
    "DROP TRIGGER IF EXISTS snapshots_no_delete",
    "DROP TRIGGER IF EXISTS snapshot_versions_insert_guard",
    "DROP TRIGGER IF EXISTS snapshot_versions_immutable",
    "DROP TRIGGER IF EXISTS snapshot_versions_no_delete",
    "DROP TRIGGER IF EXISTS active_snapshots_insert_guard",
    "DROP TRIGGER IF EXISTS active_snapshots_update_guard",
    "DROP TRIGGER IF EXISTS active_snapshots_no_delete",
)

_JOB_TRIGGERS = (
    """CREATE TRIGGER jobs_insert_guard BEFORE INSERT ON jobs WHEN
        NEW.state != 'QUEUED' OR NEW.owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL
        OR NEW.heartbeat_at IS NOT NULL OR NEW.attempt != 0
        BEGIN SELECT RAISE(ABORT, 'jobs must begin queued'); END""",
    """CREATE TRIGGER jobs_payload_immutable BEFORE UPDATE OF kind, payload,
        dedupe_key, created_at ON jobs BEGIN SELECT RAISE(ABORT, 'job payload is immutable'); END""",
    """CREATE TRIGGER jobs_state_machine BEFORE UPDATE OF state ON jobs WHEN NOT (
        (OLD.state = 'QUEUED' AND NEW.state = 'RUNNING' AND NEW.owner IS NOT NULL
            AND NEW.lease_expires_at IS NOT NULL AND NEW.heartbeat_at IS NOT NULL
            AND NEW.attempt = OLD.attempt + 1) OR
        (OLD.state = 'RUNNING' AND NEW.state IN ('SUCCEEDED','FAILED')
            AND NEW.owner = OLD.owner AND NEW.attempt = OLD.attempt) OR
        (OLD.state = 'RUNNING' AND NEW.state = 'QUEUED' AND NEW.owner IS NULL
            AND NEW.lease_expires_at IS NULL AND NEW.heartbeat_at IS NULL
            AND NEW.attempt = OLD.attempt AND julianday(OLD.lease_expires_at) <= julianday('now')))
        BEGIN SELECT RAISE(ABORT, 'invalid job state transition'); END""",
    """CREATE TRIGGER jobs_lease_guard BEFORE UPDATE OF owner, lease_expires_at,
        heartbeat_at ON jobs WHEN OLD.state = 'RUNNING' AND NEW.state = 'RUNNING'
        AND (NEW.owner != OLD.owner OR NEW.lease_expires_at IS NULL OR NEW.heartbeat_at IS NULL)
        BEGIN SELECT RAISE(ABORT, 'invalid job lease update'); END""",
)

_SNAPSHOT_TRIGGERS = (
    """CREATE TRIGGER snapshots_insert_guard BEFORE INSERT ON snapshots
        WHEN NEW.state != 'BUILDING' BEGIN SELECT RAISE(ABORT, 'snapshots begin building'); END""",
    """CREATE TRIGGER snapshots_immutable BEFORE UPDATE ON snapshots WHEN NOT
        (OLD.state = 'BUILDING' AND NEW.state = 'ACTIVE' AND NEW.id = OLD.id
        AND NEW.project_id = OLD.project_id AND NEW.generation = OLD.generation
        AND NEW.created_at = OLD.created_at)
        BEGIN SELECT RAISE(ABORT, 'snapshots are immutable'); END""",
    """CREATE TRIGGER snapshots_no_delete BEFORE DELETE ON snapshots
        BEGIN SELECT RAISE(ABORT, 'snapshots are immutable'); END""",
    """CREATE TRIGGER snapshot_versions_insert_guard BEFORE INSERT ON snapshot_versions
        WHEN NOT EXISTS (SELECT 1 FROM snapshots s JOIN documents d ON d.id = NEW.document_id
            JOIN versions v ON v.id = NEW.version_id AND v.document_id = d.id
            WHERE s.id = NEW.snapshot_id AND s.project_id = d.project_id AND s.state = 'BUILDING')
        BEGIN SELECT RAISE(ABORT, 'snapshot membership requires building snapshot'); END""",
    """CREATE TRIGGER snapshot_versions_immutable BEFORE UPDATE ON snapshot_versions
        BEGIN SELECT RAISE(ABORT, 'snapshot membership is immutable'); END""",
    """CREATE TRIGGER snapshot_versions_no_delete BEFORE DELETE ON snapshot_versions
        BEGIN SELECT RAISE(ABORT, 'snapshot membership is immutable'); END""",
    """CREATE TRIGGER active_snapshots_insert_guard BEFORE INSERT ON active_snapshots
        WHEN NOT EXISTS (SELECT 1 FROM snapshots WHERE id = NEW.snapshot_id
            AND project_id = NEW.project_id AND state = 'ACTIVE')
        BEGIN SELECT RAISE(ABORT, 'active snapshot must be active generation'); END""",
    """CREATE TRIGGER active_snapshots_update_guard BEFORE UPDATE ON active_snapshots
        WHEN NOT EXISTS (SELECT 1 FROM snapshots WHERE id = NEW.snapshot_id
            AND project_id = NEW.project_id AND state = 'ACTIVE')
        BEGIN SELECT RAISE(ABORT, 'active snapshot must be active generation'); END""",
    """CREATE TRIGGER active_snapshots_no_delete BEFORE DELETE ON active_snapshots
        BEGIN SELECT RAISE(ABORT, 'active snapshot pointer is immutable'); END""",
)

_V2_TABLE_DROPS = (
    "DROP TABLE active_snapshots",
    "DROP TABLE snapshot_versions",
    "DROP TABLE snapshots",
)
