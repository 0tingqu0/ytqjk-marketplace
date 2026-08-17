"""Validated knowledge domain operations owned by KnowledgeService."""

from __future__ import annotations

import hashlib
import sqlite3
import uuid
from typing import Any


OPERATIONS = frozenset({
    "create_project", "create_candidate", "edit_candidate",
    "soft_delete_candidate", "append_state", "create_snapshot",
    "record_feedback",
})
STATES = frozenset({"candidate", "approved", "verified", "tombstone"})
TRANSITIONS = {
    "candidate": {"candidate", "approved", "verified", "tombstone"},
    "approved": {"verified", "tombstone"},
    "verified": {"tombstone"},
    "tombstone": set(),
}
_MIRROR_MUTATIONS = frozenset({
    "append_state", "edit_candidate", "soft_delete_candidate",
})


def validate_operation(kind: str, payload: object) -> dict[str, Any]:
    """Return exact validated operation payload or reject injected fields."""
    if kind not in OPERATIONS or not isinstance(payload, dict):
        raise ValueError("unsupported operation")
    if kind == "record_feedback":
        from .feedback_domain import validate_feedback

        return validate_feedback(payload)
    required = {
        "create_project": {"id", "scope", "alias"},
        "create_candidate": {"document_id", "project_id", "title", "content", "source"},
        "edit_candidate": {"document_id", "content", "source"},
        "soft_delete_candidate": {"document_id"},
        "append_state": {"document_id", "state", "content"},
        "create_snapshot": {"project_id", "snapshot_id"},
    }[kind]
    if set(payload) != required:
        raise ValueError("operation payload fields are invalid")
    values = dict(payload)
    for name in required:
        if name.endswith("id"):
            _uuid(values[name])
    if kind == "create_project":
        _text(values["scope"])
        _text(values["alias"])
    elif kind == "create_candidate":
        _text(values["title"])
        _text(values["content"])
        _text(values["source"])
    elif kind == "edit_candidate":
        _text(values["content"])
        _text(values["source"])
    elif kind == "append_state":
        if values["state"] not in STATES - {"candidate"}:
            raise ValueError("append state is invalid")
        if values["content"] is not None:
            _text(values["content"])
    return values


def apply(connection: sqlite3.Connection, kind: str, payload: object, now: str) -> None:
    """Revalidate durable job then apply one domain operation."""
    data = validate_operation(kind, payload)
    if kind == "record_feedback":
        from .feedback_domain import apply_feedback

        apply_feedback(connection, data, now)
        return
    _guard_system_managed_mirror(connection, kind, data)
    handlers = {
        "create_project": _create_project,
        "create_candidate": _create_candidate,
        "edit_candidate": _edit_candidate,
        "soft_delete_candidate": _soft_delete_candidate,
        "append_state": _append_state,
        "create_snapshot": _create_snapshot,
    }
    handlers[kind](connection, data, now)


def _guard_system_managed_mirror(
    connection: sqlite3.Connection, kind: str, data: dict[str, Any]
) -> None:
    """Reject public mutation of a linked global mirror when schema v4 exists."""
    if kind not in _MIRROR_MUTATIONS:
        return
    table = connection.execute(
        "SELECT 1 FROM sqlite_master WHERE type = 'table' "
        "AND name = 'global_sync'"
    ).fetchone()
    if table is None:
        return
    linked = connection.execute(
        "SELECT 1 FROM global_sync WHERE global_document_id = ?",
        (data["document_id"],),
    ).fetchone()
    if linked is not None:
        raise ValueError("system-managed global mirror cannot be mutated directly")


def _create_project(connection: sqlite3.Connection, data: dict[str, Any], now: str) -> None:
    connection.execute(
        "INSERT INTO projects(id, name, scope, alias, created_at) VALUES (?, ?, ?, ?, ?)",
        (data["id"], data["alias"], data["scope"], data["alias"], now),
    )
    _audit(connection, "project_created", data["id"], now)


def _create_candidate(connection: sqlite3.Connection, data: dict[str, Any], now: str) -> None:
    connection.execute(
        "INSERT INTO documents(id, project_id, title) VALUES (?, ?, ?)",
        (data["document_id"], data["project_id"], data["title"]),
    )
    _append_version(connection, data["document_id"], "candidate", data["content"], data["source"], now)
    _audit(connection, "candidate_created", data["document_id"], now)


def _edit_candidate(connection: sqlite3.Connection, data: dict[str, Any], now: str) -> None:
    _editable(connection, data["document_id"])
    _append_version(connection, data["document_id"], "candidate", data["content"], data["source"], now)
    _audit(connection, "candidate_edited", data["document_id"], now)


def _soft_delete_candidate(connection: sqlite3.Connection, data: dict[str, Any], now: str) -> None:
    _editable(connection, data["document_id"])
    connection.execute("UPDATE documents SET deleted_at = ? WHERE id = ?", (now, data["document_id"]))
    _audit(connection, "candidate_soft_deleted", data["document_id"], now)


def _append_state(connection: sqlite3.Connection, data: dict[str, Any], now: str) -> None:
    latest = _latest(connection, data["document_id"])
    if data["state"] not in TRANSITIONS[str(latest["state"])]:
        raise ValueError("invalid governance state transition")
    content = data["content"] or _original(connection, str(latest["original_sha256"]))
    _append_version(connection, data["document_id"], data["state"], content, "governance", now)
    _audit(connection, f"version_{data['state']}", data["document_id"], now)


def _create_snapshot(connection: sqlite3.Connection, data: dict[str, Any], now: str) -> None:
    generation = connection.execute(
        "SELECT COALESCE(MAX(generation), 0) + 1 FROM snapshots WHERE project_id = ?",
        (data["project_id"],),
    ).fetchone()[0]
    connection.execute(
        "INSERT INTO snapshots(id, project_id, generation, state, created_at) VALUES (?, ?, ?, 'BUILDING', ?)",
        (data["snapshot_id"], data["project_id"], generation, now),
    )
    rows = connection.execute(
        "SELECT d.id, v.id FROM documents d JOIN versions v ON v.document_id = d.id "
        "WHERE d.project_id = ? AND d.deleted_at IS NULL AND v.ordinal = "
        "(SELECT MAX(ordinal) FROM versions WHERE document_id = d.id) AND v.state != 'tombstone'",
        (data["project_id"],),
    ).fetchall()
    connection.executemany(
        "INSERT INTO snapshot_versions(snapshot_id, document_id, version_id) VALUES (?, ?, ?)",
        [(data["snapshot_id"], row[0], row[1]) for row in rows],
    )
    connection.execute("UPDATE snapshots SET state = 'ACTIVE' WHERE id = ?", (data["snapshot_id"],))
    connection.execute(
        "INSERT INTO active_snapshots(project_id, snapshot_id) VALUES (?, ?) "
        "ON CONFLICT(project_id) DO UPDATE SET snapshot_id = excluded.snapshot_id",
        (data["project_id"], data["snapshot_id"]),
    )
    _audit(connection, "snapshot_activated", data["snapshot_id"], now)


def _append_version(
    connection: sqlite3.Connection,
    document_id: str,
    state: str,
    content: str,
    source: str,
    now: str,
    *,
    source_kind: str = "local",
) -> int:
    digest = hashlib.sha256(content.encode("utf-8")).hexdigest()
    connection.execute(
        "INSERT OR IGNORE INTO originals(sha256, content, created_at) VALUES (?, ?, ?)",
        (digest, content.encode("utf-8"), now),
    )
    ordinal = connection.execute(
        "SELECT COALESCE(MAX(ordinal), 0) + 1 FROM versions WHERE document_id = ?", (document_id,)
    ).fetchone()[0]
    version_id = connection.execute(
        "INSERT INTO versions(document_id, ordinal, state, original_sha256, created_at) VALUES (?, ?, ?, ?, ?)",
        (document_id, ordinal, state, digest, now),
    ).lastrowid
    chunk_id = hashlib.sha256(f"{version_id}:1:{digest}".encode()).hexdigest()
    connection.execute("INSERT INTO chunks(id, version_id, ordinal, content) VALUES (?, ?, 1, ?)", (chunk_id, version_id, content))
    connection.execute(
        "INSERT INTO sources(version_id, kind, locator) VALUES (?, ?, ?)",
        (version_id, source_kind, source),
    )
    connection.execute("INSERT INTO governance(version_id, action, actor, created_at) VALUES (?, ?, ?, ?)", (version_id, state, "一听曲就困", now))
    return int(version_id)


def _editable(connection: sqlite3.Connection, document_id: str) -> None:
    document = connection.execute("SELECT deleted_at FROM documents WHERE id = ?", (document_id,)).fetchone()
    if document is None or document["deleted_at"] is not None or _latest(connection, document_id)["state"] != "candidate":
        raise ValueError("only active candidate revisions are editable")


def _latest(connection: sqlite3.Connection, document_id: str) -> sqlite3.Row:
    row = connection.execute("SELECT * FROM versions WHERE document_id = ? ORDER BY ordinal DESC LIMIT 1", (document_id,)).fetchone()
    if row is None:
        raise ValueError("document has no versions")
    return row


def _original(connection: sqlite3.Connection, digest: str) -> str:
    row = connection.execute("SELECT content FROM originals WHERE sha256 = ?", (digest,)).fetchone()
    if row is None:
        raise ValueError("original is unavailable")
    return bytes(row[0]).decode("utf-8")


def _audit(connection: sqlite3.Connection, event: str, subject_id: str, now: str) -> None:
    connection.execute("INSERT INTO audit(event, subject_id, created_at, detail) VALUES (?, ?, ?, ?)", (event, subject_id, now, "{}"))


def _text(value: object) -> None:
    if not isinstance(value, str) or not value.strip():
        raise ValueError("text field is required")


def _uuid(value: object) -> None:
    if not isinstance(value, str):
        raise ValueError("identifier must be UUID")
    try:
        uuid.UUID(value)
    except ValueError as error:
        raise ValueError("identifier must be UUID") from error
