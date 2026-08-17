"""Canonical schema metadata for feedback governance tables."""

from __future__ import annotations


TABLES = {
    "feedback_events": """CREATE TABLE feedback_events (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        job_id INTEGER NOT NULL UNIQUE REFERENCES jobs(id),
        document_id TEXT NOT NULL REFERENCES documents(id),
        invocation_id TEXT NOT NULL, correct INTEGER NOT NULL CHECK(correct IN (0, 1)),
        score INTEGER NOT NULL CHECK(score BETWEEN 0 AND 3),
        state TEXT NOT NULL CHECK(state IN
            ('candidate','approved','verified','tombstone')),
        input_version_id INTEGER NOT NULL REFERENCES versions(id),
        result_version_id INTEGER NOT NULL REFERENCES versions(id),
        global_result_version_id INTEGER REFERENCES versions(id),
        created_at TEXT NOT NULL, UNIQUE(document_id, invocation_id),
        CHECK((score = 0 AND state = 'tombstone') OR
            (score = 1 AND state = 'candidate') OR
            (score = 2 AND state = 'approved') OR
            (score = 3 AND state = 'verified'))
    )""",
    "global_sync": """CREATE TABLE global_sync (
        source_document_id TEXT PRIMARY KEY REFERENCES documents(id),
        global_document_id TEXT NOT NULL UNIQUE REFERENCES documents(id),
        created_at TEXT NOT NULL
    )""",
}

COLUMNS = {
    "feedback_events": (
        ("id", "INTEGER", 0, None, 1, 0),
        ("job_id", "INTEGER", 1, None, 0, 0),
        ("document_id", "TEXT", 1, None, 0, 0),
        ("invocation_id", "TEXT", 1, None, 0, 0),
        ("correct", "INTEGER", 1, None, 0, 0),
        ("score", "INTEGER", 1, None, 0, 0),
        ("state", "TEXT", 1, None, 0, 0),
        ("input_version_id", "INTEGER", 1, None, 0, 0),
        ("result_version_id", "INTEGER", 1, None, 0, 0),
        ("global_result_version_id", "INTEGER", 0, None, 0, 0),
        ("created_at", "TEXT", 1, None, 0, 0),
    ),
    "global_sync": (
        ("source_document_id", "TEXT", 0, None, 1, 0),
        ("global_document_id", "TEXT", 1, None, 0, 0),
        ("created_at", "TEXT", 1, None, 0, 0),
    ),
}

UNIQUE_KEYS = {
    "jobs": {("u", 0, ("dedupe_key",))},
    "feedback_events": {
        ("u", 0, ("job_id",)),
        ("u", 0, ("document_id", "invocation_id")),
    },
    "global_sync": {
        ("pk", 0, ("source_document_id",)),
        ("u", 0, ("global_document_id",)),
    },
}

FOREIGN_KEYS = {
    "jobs": set(),
    "feedback_events": {
        ("job_id", "jobs", "id", "NO ACTION", "NO ACTION", "NONE"),
        ("document_id", "documents", "id", "NO ACTION", "NO ACTION", "NONE"),
        ("input_version_id", "versions", "id", "NO ACTION", "NO ACTION", "NONE"),
        ("result_version_id", "versions", "id", "NO ACTION", "NO ACTION", "NONE"),
        (
            "global_result_version_id",
            "versions",
            "id",
            "NO ACTION",
            "NO ACTION",
            "NONE",
        ),
    },
    "global_sync": {
        ("source_document_id", "documents", "id", "NO ACTION", "NO ACTION", "NONE"),
        ("global_document_id", "documents", "id", "NO ACTION", "NO ACTION", "NONE"),
    },
}
