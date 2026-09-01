package library

const librarySchemaVersion = 1

const librarySchema = `
CREATE TABLE IF NOT EXISTS library_state (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL CHECK (revision >= 0),
    digest TEXT NOT NULL,
    nodes_json BLOB NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS library_previews (
    digest TEXT PRIMARY KEY,
    action TEXT NOT NULL,
    payload_json BLOB NOT NULL,
    base_revision INTEGER NOT NULL CHECK (base_revision >= 0),
    base_digest TEXT NOT NULL,
    target_digest TEXT NOT NULL,
    preview_json BLOB NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('ACTIVE', 'CONSUMED')),
    created_at TEXT NOT NULL,
    consumed_revision INTEGER
);
CREATE INDEX IF NOT EXISTS library_previews_base_revision
    ON library_previews(base_revision, state);
`
