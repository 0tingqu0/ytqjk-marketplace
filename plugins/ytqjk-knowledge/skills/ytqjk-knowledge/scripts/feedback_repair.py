"""Fail-closed causal validation for persisted feedback history."""

from __future__ import annotations

import json
import sqlite3
from typing import Any

from .feedback_domain import feedback_outcome, validate_feedback


Event = sqlite3.Row | tuple[Any, ...]
Version = tuple[int, str, int, str, str]
Mirror = tuple[int, str, str, str, int]
_STATE_SCORES = {"candidate": 0, "approved": 2, "verified": 3}


def feedback_history_error(connection: sqlite3.Connection) -> str | None:
    """Return the first causal violation in stable event-PK order."""
    previous: dict[str, tuple[int, int]] = {}
    mirrors: dict[str, Mirror] = {}
    feedback_versions: list[int] = []
    event_jobs: list[int] = []
    events = connection.execute(
        "SELECT id, job_id, document_id, invocation_id, correct, score, state, "
        "input_version_id, result_version_id, global_result_version_id "
        "FROM feedback_events ORDER BY id"
    ).fetchall()
    for event in events:
        document_id = str(event[2])
        payload_error = _job_error(connection, event, document_id)
        if payload_error:
            return payload_error
        event_jobs.append(int(event[1]))
        input_version = _version(connection, int(event[7]))
        result_version = _version(connection, int(event[8]))
        scope = connection.execute(
            "SELECT p.scope FROM documents d JOIN projects p ON p.id = d.project_id "
            "WHERE d.id = ?",
            (document_id,),
        ).fetchone()
        if scope is None or scope[0] == "global":
            return "feedback document scope"
        if input_version[1] != document_id or result_version[1] != document_id:
            return "feedback version document"
        prior_score, prior_ordinal = previous.get(document_id, (0, 0))
        input_error = _input_error(
            connection, input_version, document_id, prior_ordinal
        )
        if input_error:
            return input_error
        score, state = feedback_outcome(
            max(prior_score, _STATE_SCORES[input_version[3]]),
            input_version[3],
            bool(event[4]),
        )
        if (int(event[5]), str(event[6])) != (score, state):
            return "feedback trajectory"
        result_error = _result_error(
            connection, input_version, result_version, state, document_id
        )
        if result_error:
            return result_error
        if result_version[0] != input_version[0]:
            feedback_versions.append(result_version[0])
        mirror_error, mirror = _mirror_error(
            connection, event, input_version, mirrors.get(document_id)
        )
        if mirror_error:
            return mirror_error
        if mirror is not None:
            mirrors[document_id] = mirror
        previous[document_id] = (score, result_version[2])
    return _orphan_error(connection, event_jobs, feedback_versions, mirrors)


def _job_error(
    connection: sqlite3.Connection, event: Event, document_id: str
) -> str | None:
    try:
        expected = validate_feedback(
            {
                "document_id": document_id,
                "invocation_id": str(event[3]),
                "correct": bool(event[4]),
            }
        )
    except ValueError:
        return "invocation UUID"
    job = connection.execute(
        "SELECT kind, payload, state, attempt, started_at, finished_at, owner, "
        "error FROM jobs WHERE id = ?",
        (event[1],),
    ).fetchone()
    valid_state = (
        job is not None
        and job[0] == "record_feedback"
        and job[2] == "SUCCEEDED"
        and int(job[3]) >= 1
        and all(job[index] is not None for index in (4, 5, 6))
        and job[7] is None
    )
    if not valid_state:
        return "feedback job state"
    try:
        payload = validate_feedback(json.loads(str(job[1])))
    except (json.JSONDecodeError, TypeError, ValueError):
        return "feedback job payload"
    return None if payload == expected else "feedback job payload"


def _input_error(
    connection: sqlite3.Connection,
    input_version: Version,
    document_id: str,
    prior_ordinal: int,
) -> str | None:
    if input_version[2] < prior_ordinal:
        return "feedback version order"
    if input_version[3] not in _STATE_SCORES:
        return "feedback tombstone input"
    orphan = connection.execute(
        "SELECT 1 FROM versions v JOIN sources s ON s.version_id = v.id "
        "WHERE v.document_id = ? AND v.ordinal > ? AND v.ordinal <= ? "
        "AND s.kind = 'feedback' LIMIT 1",
        (document_id, prior_ordinal, input_version[2]),
    ).fetchone()
    return "feedback input causality" if orphan else None


def _result_error(
    connection: sqlite3.Connection,
    input_version: Version,
    result_version: Version,
    state: str,
    document_id: str,
) -> str | None:
    if state == input_version[3]:
        if result_version[0] == input_version[0]:
            return None
        return "feedback unchanged result"
    sources = [
        (str(row[0]), str(row[1]))
        for row in connection.execute(
            "SELECT kind, locator FROM sources WHERE version_id = ?",
            (result_version[0],),
        )
    ]
    valid = (
        result_version[2] == input_version[2] + 1
        and result_version[3:] == (state, input_version[4])
        and sources == [("feedback", f"feedback:{document_id}")]
    )
    return None if valid else "feedback result causality"


def _mirror_error(
    connection: sqlite3.Connection,
    event: Event,
    input_version: Version,
    prior: Mirror | None,
) -> tuple[str | None, Mirror | None]:
    document_id = input_version[1]
    result_id = int(event[9]) if event[9] is not None else None
    link = connection.execute(
        "SELECT global_document_id FROM global_sync WHERE source_document_id = ?",
        (document_id,),
    ).fetchone()
    first_sync = prior is None and bool(event[4]) and int(event[5]) >= 2
    if prior is None and not first_sync:
        if result_id is not None:
            return "unexpected global feedback result", None
        return None, None
    if link is None or result_id is None:
        return "missing global feedback result", prior
    global_id = str(link[0])
    start_id, prior_global_id, start_state, start_sha, start_ordinal = prior or (
        0,
        global_id,
        "candidate",
        input_version[4],
        0,
    )
    if prior_global_id != global_id:
        return "global mirror link changed", prior
    expected: list[tuple[str, str]] = []
    if prior is None:
        expected.append(("candidate", input_version[4]))
    elif start_sha != input_version[4]:
        expected.append((start_state, input_version[4]))
    current_state = expected[-1][0] if expected else start_state
    if current_state != event[6]:
        expected.append((str(event[6]), input_version[4]))
    rows = connection.execute(
        "SELECT v.id, v.state, v.original_sha256, v.ordinal, s.kind, s.locator "
        "FROM versions v JOIN sources s ON s.version_id = v.id "
        "WHERE v.document_id = ? AND v.ordinal > ? AND v.ordinal <= "
        "(SELECT ordinal FROM versions WHERE id = ?) ORDER BY v.ordinal",
        (global_id, start_ordinal, result_id),
    ).fetchall()
    signatures = [(str(row[1]), str(row[2])) for row in rows]
    source = ("project-sync", f"project-sync:{document_id}")
    if signatures != expected or any((row[4], row[5]) != source for row in rows):
        return "global mirror suffix", prior
    if expected:
        if not rows or int(rows[-1][0]) != result_id:
            return "global mirror result", prior
        final = rows[-1]
        return None, (
            int(final[0]),
            global_id,
            str(final[1]),
            str(final[2]),
            int(final[3]),
        )
    if result_id != start_id:
        return "global mirror unchanged result", prior
    return None, prior


def _orphan_error(
    connection: sqlite3.Connection,
    event_jobs: list[int],
    feedback_versions: list[int],
    mirrors: dict[str, Mirror],
) -> str | None:
    stored_versions = [
        int(row[0])
        for row in connection.execute(
            "SELECT v.id FROM versions v JOIN sources s ON s.version_id = v.id "
            "WHERE s.kind = 'feedback' ORDER BY v.id"
        )
    ]
    if stored_versions != sorted(feedback_versions):
        return "orphaned feedback version"
    unowned_downgrade = connection.execute(
        "SELECT 1 FROM versions current JOIN versions prior ON "
        "prior.document_id = current.document_id AND "
        "prior.ordinal = current.ordinal - 1 JOIN documents d ON "
        "d.id = current.document_id JOIN projects p ON p.id = d.project_id "
        "WHERE p.scope != 'global' AND ((prior.state = 'approved' AND "
        "current.state = 'candidate') OR (prior.state = 'verified' AND "
        "current.state = 'approved')) AND NOT EXISTS (SELECT 1 FROM sources s "
        "WHERE s.version_id = current.id AND s.kind = 'feedback') LIMIT 1"
    ).fetchone()
    if unowned_downgrade:
        return "orphaned feedback transition"
    stored_jobs = [
        int(row[0])
        for row in connection.execute(
            "SELECT id FROM jobs WHERE kind = 'record_feedback' "
            "AND state = 'SUCCEEDED' ORDER BY id"
        )
    ]
    if stored_jobs != sorted(event_jobs):
        return "orphaned feedback job"
    links = {
        str(row[0])
        for row in connection.execute("SELECT source_document_id FROM global_sync")
    }
    if links != set(mirrors):
        return "global sync event causality"
    for _, global_id, _, _, ordinal in mirrors.values():
        latest = connection.execute(
            "SELECT MAX(ordinal) FROM versions WHERE document_id = ?", (global_id,)
        ).fetchone()[0]
        if int(latest) != ordinal:
            return "global mirror tail"
    return None


def _version(connection: sqlite3.Connection, version_id: int) -> Version:
    row = connection.execute(
        "SELECT id, document_id, ordinal, state, original_sha256 FROM versions "
        "WHERE id = ?",
        (version_id,),
    ).fetchone()
    if row is None:
        raise sqlite3.DatabaseError("feedback version is missing")
    return int(row[0]), str(row[1]), int(row[2]), str(row[3]), str(row[4])
