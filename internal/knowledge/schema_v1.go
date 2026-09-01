package knowledge

var schemaV1Statements = splitStatements(`
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, scope TEXT NOT NULL,
  alias TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS projects_scope_alias ON projects(scope, alias);
CREATE TABLE IF NOT EXISTS originals (
  sha256 TEXT PRIMARY KEY, content BLOB NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS documents (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  title TEXT NOT NULL, deleted_at TEXT
);
CREATE TABLE IF NOT EXISTS versions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  document_id TEXT NOT NULL REFERENCES documents(id), ordinal INTEGER NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('candidate','approved','verified','tombstone')),
  original_sha256 TEXT NOT NULL REFERENCES originals(sha256),
  created_at TEXT NOT NULL, UNIQUE(document_id, ordinal)
);
CREATE TABLE IF NOT EXISTS chunks (
  id TEXT PRIMARY KEY, version_id INTEGER NOT NULL REFERENCES versions(id),
  ordinal INTEGER NOT NULL, content TEXT NOT NULL, UNIQUE(version_id, ordinal)
);
CREATE TABLE IF NOT EXISTS sources (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  version_id INTEGER NOT NULL REFERENCES versions(id), kind TEXT NOT NULL,
  locator TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS governance (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  version_id INTEGER NOT NULL REFERENCES versions(id), action TEXT NOT NULL,
  actor TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT, event TEXT NOT NULL,
  subject_id TEXT NOT NULL, created_at TEXT NOT NULL, detail TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL CHECK(kind IN ('create_project','create_candidate','edit_candidate','soft_delete_candidate','append_state','create_snapshot')),
  payload TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('QUEUED','RUNNING','SUCCEEDED','FAILED')),
  dedupe_key TEXT UNIQUE, error TEXT, created_at TEXT NOT NULL, started_at TEXT,
  finished_at TEXT, owner TEXT, lease_expires_at TEXT, heartbeat_at TEXT,
  attempt INTEGER NOT NULL DEFAULT 0
);
CREATE TRIGGER IF NOT EXISTS projects_immutable BEFORE UPDATE OF id, scope, alias ON projects
BEGIN SELECT RAISE(ABORT, 'project identity is immutable'); END;
CREATE TRIGGER IF NOT EXISTS documents_soft_delete_candidate BEFORE UPDATE OF deleted_at ON documents
WHEN NOT (OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND EXISTS (
  SELECT 1 FROM versions WHERE document_id=OLD.id AND ordinal=(
    SELECT MAX(ordinal) FROM versions WHERE document_id=OLD.id) AND state='candidate'))
BEGIN SELECT RAISE(ABORT, 'only active candidates can be soft deleted'); END;
CREATE TRIGGER IF NOT EXISTS originals_immutable_update BEFORE UPDATE ON originals
BEGIN SELECT RAISE(ABORT, 'originals are immutable'); END;
CREATE TRIGGER IF NOT EXISTS originals_immutable_delete BEFORE DELETE ON originals
BEGIN SELECT RAISE(ABORT, 'originals are immutable'); END;
CREATE TRIGGER IF NOT EXISTS versions_append_only BEFORE UPDATE ON versions
BEGIN SELECT RAISE(ABORT, 'versions are append-only'); END;
CREATE TRIGGER IF NOT EXISTS versions_no_delete BEFORE DELETE ON versions
BEGIN SELECT RAISE(ABORT, 'versions are append-only'); END;
CREATE TRIGGER IF NOT EXISTS versions_state_machine BEFORE INSERT ON versions
WHEN NEW.ordinal != COALESCE((SELECT MAX(ordinal)+1 FROM versions
  WHERE document_id=NEW.document_id),1) OR NOT (
  (NEW.ordinal=1 AND NEW.state='candidate') OR EXISTS (
    SELECT 1 FROM versions prior WHERE prior.document_id=NEW.document_id
    AND prior.ordinal=NEW.ordinal-1 AND (
      (prior.state='candidate' AND NEW.state IN ('candidate','approved','verified','tombstone')) OR
      (prior.state='approved' AND NEW.state IN ('candidate','approved','verified','tombstone')) OR
      (prior.state='verified' AND NEW.state IN ('approved','verified','tombstone')))))
BEGIN SELECT RAISE(ABORT, 'invalid governance state transition'); END;
CREATE TRIGGER IF NOT EXISTS audit_immutable_update BEFORE UPDATE ON audit
BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
CREATE TRIGGER IF NOT EXISTS audit_immutable_delete BEFORE DELETE ON audit
BEGIN SELECT RAISE(ABORT, 'audit is append-only'); END;
CREATE TRIGGER IF NOT EXISTS jobs_insert_guard BEFORE INSERT ON jobs WHEN
  NEW.state!='QUEUED' OR NEW.owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL
  OR NEW.heartbeat_at IS NOT NULL OR NEW.attempt!=0
BEGIN SELECT RAISE(ABORT, 'jobs must begin queued'); END;
CREATE TRIGGER IF NOT EXISTS jobs_payload_immutable BEFORE UPDATE OF kind,payload,dedupe_key,created_at ON jobs
BEGIN SELECT RAISE(ABORT, 'job payload is immutable'); END;
CREATE TRIGGER IF NOT EXISTS jobs_state_machine BEFORE UPDATE OF state ON jobs WHEN NOT (
  (OLD.state='QUEUED' AND NEW.state='RUNNING' AND NEW.owner IS NOT NULL
    AND NEW.lease_expires_at IS NOT NULL AND NEW.heartbeat_at IS NOT NULL
    AND NEW.attempt=OLD.attempt+1) OR
  (OLD.state='RUNNING' AND NEW.state IN ('SUCCEEDED','FAILED')
    AND NEW.owner=OLD.owner AND NEW.attempt=OLD.attempt) OR
  (OLD.state='RUNNING' AND NEW.state='QUEUED' AND NEW.owner IS NULL
    AND NEW.lease_expires_at IS NULL AND NEW.heartbeat_at IS NULL
    AND NEW.attempt=OLD.attempt AND (
      OLD.owner IS NULL OR OLD.heartbeat_at IS NULL OR OLD.lease_expires_at IS NULL
      OR julianday(OLD.lease_expires_at)<=julianday('now'))))
BEGIN SELECT RAISE(ABORT, 'invalid job state transition'); END;
CREATE TRIGGER IF NOT EXISTS jobs_lease_guard BEFORE UPDATE OF owner,lease_expires_at,heartbeat_at ON jobs
WHEN OLD.state='RUNNING' AND NEW.state='RUNNING' AND (
  NEW.owner!=OLD.owner OR NEW.lease_expires_at IS NULL OR NEW.heartbeat_at IS NULL)
BEGIN SELECT RAISE(ABORT, 'invalid job lease update'); END;
`)
