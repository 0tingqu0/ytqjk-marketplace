"""Atomic feedback lifecycle and project-to-global synchronization."""

from __future__ import annotations

import json
import sqlite3
import uuid
from typing import Any

from .domain import _append_version, _audit


GLOBAL_ALIAS = "global-knowledge"
GLOBAL_SCOPE = "global"
GLOBAL_NAMESPACE = uuid.UUID("afca7743-836a-4fb3-a7aa-31f010f58eb0")
_PROJECT_TRANSITIONS = {
    None: {"candidate"},
    "candidate": {"candidate", "approved", "verified", "tombstone"},
    "approved": {"candidate", "verified", "tombstone"},
    "verified": {"approved", "tombstone"},
    "tombstone": set(),
}
_GLOBAL_TRANSITIONS = {
    None: {"candidate"},
    "candidate": {"candidate", "approved", "verified", "tombstone"},
    "approved": {"candidate", "approved", "verified", "tombstone"},
    "verified": {"approved", "verified", "tombstone"},
    "tombstone": set(),
}


def validate_feedback(payload: object) -> dict[str, Any]:
    """Return exact validated feedback payload."""
    if not isinstance(payload, dict):
        raise ValueError("feedback payload must be an object")
    required = {"document_id", "invocation_id", "correct"}
    if set(payload) != required:
        raise ValueError("feedback payload fields are invalid")
    document_id = _uuid(payload["document_id"], "document_id")
    invocation_id = _uuid(payload["invocation_id"], "invocation_id")
    if not isinstance(payload["correct"], bool):
        raise ValueError("feedback correctness must be boolean")
    return {
        "document_id": document_id,
        "invocation_id": invocation_id,
        "correct": payload["correct"],
    }


def invalid_version_history(
    connection: sqlite3.Connection, *, allow_feedback: bool = True
) -> bool:
    """Return whether persisted versions violate the v4 lifecycle graph."""
    project_transitions = _PROJECT_TRANSITIONS
    global_transitions = _GLOBAL_TRANSITIONS
    if not allow_feedback:
        project_transitions = {
            None: {"candidate"},
            "candidate": {"candidate", "approved", "verified", "tombstone"},
            "approved": {"verified", "tombstone"},
            "verified": {"tombstone"},
            "tombstone": set(),
        }
        global_transitions = project_transitions
    previous: dict[str, tuple[int, str]] = {}
    rows = connection.execute(
        "SELECT v.document_id, v.ordinal, v.state, p.scope FROM versions v "
        "JOIN documents d ON d.id = v.document_id "
        "JOIN projects p ON p.id = d.project_id ORDER BY v.document_id, v.ordinal"
    )
    for document_id, ordinal, state, scope in rows:
        prior = previous.get(str(document_id))
        if int(ordinal) != (prior[0] + 1 if prior else 1):
            return True
        transitions = (
            global_transitions if scope == GLOBAL_SCOPE else project_transitions
        )
        if str(state) not in transitions[prior[1] if prior else None]:
            return True
        previous[str(document_id)] = (int(ordinal), str(state))
    return False


def apply_feedback(
    connection: sqlite3.Connection, data: dict[str, Any], now: str
) -> None:
    """Record one invocation result and apply its lifecycle atomically."""
    job_id = _running_feedback_job(connection, data)
    existing = connection.execute(
        "SELECT 1 FROM feedback_events WHERE document_id = ? "
        "AND invocation_id = ?",
        (data["document_id"], data["invocation_id"]),
    ).fetchone()
    if existing is not None:
        raise ValueError("invocation feedback conflicts with existing event")
    source = _document(connection, data["document_id"])
    if source["scope"] == GLOBAL_SCOPE:
        raise ValueError("feedback must target a project knowledge document")
    if source["state"] == "tombstone":
        raise ValueError("recycled knowledge cannot receive feedback")
    score = _score(connection, data["document_id"], source["state"])
    next_score, next_state = feedback_outcome(
        score, source["state"], data["correct"]
    )
    global_id = _linked_document(connection, data["document_id"])
    if global_id is None and data["correct"] and next_score >= 2:
        global_id = _create_global_copy(connection, source, now)
    result_version_id = _transition(
        connection, data["document_id"], next_state, source, now
    )
    global_result_version_id = None
    if global_id is not None:
        global_result_version_id = _sync_global(
            connection, global_id, next_state, source, now
        )
    connection.execute(
        "INSERT INTO feedback_events(job_id, document_id, invocation_id, correct, "
        "score, state, input_version_id, result_version_id, "
        "global_result_version_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (
            job_id, data["document_id"], data["invocation_id"],
            int(data["correct"]), next_score, next_state, source["version_id"],
            result_version_id, global_result_version_id, now,
        ),
    )
    _audit(
        connection,
        "feedback_correct" if data["correct"] else "feedback_incorrect",
        data["document_id"],
        now,
    )


def feedback_outcome(score: int, state: str, correct: bool) -> tuple[int, str]:
    """Return the governed score and state for one explicit result."""
    if correct:
        next_score = min(3, score + 1)
        state_by_score = {0: "candidate", 1: "candidate", 2: "approved", 3: "verified"}
        return next_score, state_by_score[next_score]
    if state == "candidate":
        return 0, "tombstone"
    if state == "approved":
        return 1, "candidate"
    return 2, "approved"


def _score(
    connection: sqlite3.Connection, document_id: str, state: str
) -> int:
    row = connection.execute(
        "SELECT score FROM feedback_events WHERE document_id = ? "
        "ORDER BY id DESC LIMIT 1", (document_id,),
    ).fetchone()
    event_score = int(row[0]) if row else 0
    state_score = {"candidate": 0, "approved": 2, "verified": 3}[state]
    return max(event_score, state_score)


def _document(connection: sqlite3.Connection, document_id: str) -> dict[str, Any]:
    row = connection.execute(
        "SELECT d.id, d.title, p.scope, v.id AS version_id, v.ordinal, v.state, "
        "o.content, v.original_sha256 "
        "FROM documents d JOIN projects p ON p.id = d.project_id "
        "JOIN versions v ON v.document_id = d.id "
        "JOIN originals o ON o.sha256 = v.original_sha256 "
        "WHERE d.id = ? AND d.deleted_at IS NULL "
        "ORDER BY v.ordinal DESC LIMIT 1", (document_id,),
    ).fetchone()
    if row is None:
        raise ValueError("knowledge document is unavailable")
    result = dict(row)
    result["content"] = bytes(result["content"]).decode("utf-8")
    return result


def _create_global_copy(
    connection: sqlite3.Connection, source: dict[str, Any], now: str
) -> str:
    project = connection.execute(
        "SELECT id, scope FROM projects WHERE alias = ?", (GLOBAL_ALIAS,),
    ).fetchone()
    if project is None:
        project_id = str(uuid.uuid5(GLOBAL_NAMESPACE, GLOBAL_ALIAS))
        connection.execute(
            "INSERT INTO projects(id, name, scope, alias, created_at) "
            "VALUES (?, ?, ?, ?, ?)",
            (project_id, GLOBAL_ALIAS, GLOBAL_SCOPE, GLOBAL_ALIAS, now),
        )
        _audit(connection, "project_created", project_id, now)
    else:
        project_id = str(project["id"])
        if project["scope"] != GLOBAL_SCOPE:
            raise ValueError("global knowledge project identity is invalid")
    global_id = str(uuid.uuid5(uuid.UUID(project_id), source["id"]))
    connection.execute(
        "INSERT INTO documents(id, project_id, title) VALUES (?, ?, ?)",
        (global_id, project_id, source["title"]),
    )
    _append_version(
        connection,
        global_id,
        "candidate",
        source["content"],
        f"project-sync:{source['id']}",
        now,
        source_kind="project-sync",
    )
    connection.execute(
        "INSERT INTO global_sync(source_document_id, global_document_id, created_at) "
        "VALUES (?, ?, ?)", (source["id"], global_id, now),
    )
    _audit(connection, "knowledge_synced_global", source["id"], now)
    return global_id


def _linked_document(
    connection: sqlite3.Connection, source_document_id: str
) -> str | None:
    row = connection.execute(
        "SELECT global_document_id FROM global_sync WHERE source_document_id = ?",
        (source_document_id,),
    ).fetchone()
    return str(row[0]) if row else None


def _transition(
    connection: sqlite3.Connection, document_id: str, state: str,
    source: dict[str, Any], now: str,
) -> int:
    if source["state"] != state:
        return _append_version(
            connection,
            document_id,
            state,
            source["content"],
            f"feedback:{document_id}",
            now,
            source_kind="feedback",
        )
    return int(source["version_id"])


def _sync_global(
    connection: sqlite3.Connection, global_id: str, state: str,
    source: dict[str, Any], now: str,
) -> int:
    current = _document(connection, global_id)
    if current["original_sha256"] != source["original_sha256"]:
        _append_version(
            connection,
            global_id,
            current["state"],
            source["content"],
            f"project-sync:{source['id']}",
            now,
            source_kind="project-sync",
        )
        current = _document(connection, global_id)
    if current["state"] != state:
        return _append_version(
            connection,
            global_id,
            state,
            source["content"],
            f"project-sync:{source['id']}",
            now,
            source_kind="project-sync",
        )
    return int(current["version_id"])


def _running_feedback_job(
    connection: sqlite3.Connection, data: dict[str, Any]
) -> int:
    rows = connection.execute(
        "SELECT id, kind, payload FROM jobs WHERE state = 'RUNNING'"
    ).fetchall()
    if len(rows) != 1 or rows[0][1] != "record_feedback":
        raise RuntimeError("feedback requires exactly one running feedback job")
    try:
        payload = validate_feedback(json.loads(str(rows[0][2])))
    except (json.JSONDecodeError, TypeError, ValueError) as error:
        raise RuntimeError("running feedback job payload is invalid") from error
    if payload != data:
        raise RuntimeError("running feedback job payload does not match")
    return int(rows[0][0])


def _uuid(value: object, name: str) -> str:
    if not isinstance(value, str):
        raise ValueError(f"{name} must be canonical UUID")
    try:
        canonical = str(uuid.UUID(value))
    except ValueError as error:
        raise ValueError(f"{name} must be canonical UUID") from error
    if value != canonical:
        raise ValueError(f"{name} must be canonical UUID")
    return canonical
