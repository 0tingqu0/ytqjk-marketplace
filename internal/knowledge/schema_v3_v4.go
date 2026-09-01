package knowledge

var schemaV3Statements = splitStatements(`
CREATE TABLE IF NOT EXISTS import_documents (
  project_id TEXT NOT NULL REFERENCES projects(id), content_sha256 TEXT NOT NULL,
  document_id TEXT NOT NULL UNIQUE REFERENCES documents(id),
  version_id INTEGER NOT NULL UNIQUE REFERENCES versions(id),
  PRIMARY KEY(project_id, content_sha256)
);
CREATE TABLE IF NOT EXISTS import_provenance (
  document_id TEXT NOT NULL REFERENCES documents(id), source_kind TEXT NOT NULL,
  source_ref TEXT NOT NULL, source_sha256 TEXT NOT NULL, scanner TEXT NOT NULL,
  scan_state TEXT NOT NULL CHECK(scan_state = 'CLEAN'),
  governance_state TEXT NOT NULL DEFAULT 'CANDIDATE' CHECK(governance_state = 'CANDIDATE'),
  PRIMARY KEY(document_id, source_kind, source_ref)
);
CREATE TABLE IF NOT EXISTS import_receipts (
  marker TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  receipt TEXT NOT NULL, receipt_sha256 TEXT NOT NULL, completed_at TEXT NOT NULL
);
`)

var schemaV4Statements = splitStatements(`
CREATE TABLE IF NOT EXISTS feedback_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL CHECK(kind = 'record_feedback'),
  payload TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('QUEUED','RUNNING','SUCCEEDED','FAILED')),
  dedupe_key TEXT UNIQUE, error TEXT, created_at TEXT NOT NULL, started_at TEXT,
  finished_at TEXT, owner TEXT, lease_expires_at TEXT, heartbeat_at TEXT,
  attempt INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS feedback_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id INTEGER NOT NULL UNIQUE REFERENCES feedback_jobs(id),
  document_id TEXT NOT NULL REFERENCES documents(id), invocation_id TEXT NOT NULL,
  correct INTEGER NOT NULL CHECK(correct IN (0,1)), score INTEGER NOT NULL CHECK(score BETWEEN 0 AND 3),
  state TEXT NOT NULL CHECK(state IN ('candidate','approved','verified','tombstone')),
  input_version_id INTEGER NOT NULL REFERENCES versions(id),
  result_version_id INTEGER NOT NULL REFERENCES versions(id),
  global_result_version_id INTEGER REFERENCES versions(id), created_at TEXT NOT NULL,
  UNIQUE(document_id, invocation_id),
  CHECK((score=0 AND state='tombstone') OR
        (score=1 AND state='candidate') OR
        (score=2 AND state='approved') OR
        (score=3 AND state='verified'))
);
CREATE TABLE IF NOT EXISTS global_sync (
  source_document_id TEXT PRIMARY KEY REFERENCES documents(id),
  global_document_id TEXT NOT NULL UNIQUE REFERENCES documents(id), created_at TEXT NOT NULL
);
CREATE TRIGGER IF NOT EXISTS feedback_jobs_insert_guard BEFORE INSERT ON feedback_jobs WHEN
  NEW.state!='QUEUED' OR NEW.owner IS NOT NULL OR NEW.lease_expires_at IS NOT NULL
  OR NEW.heartbeat_at IS NOT NULL OR NEW.attempt!=0
BEGIN SELECT RAISE(ABORT, 'feedback jobs must begin queued'); END;
CREATE TRIGGER IF NOT EXISTS feedback_jobs_payload_immutable
BEFORE UPDATE OF kind,payload,dedupe_key,created_at ON feedback_jobs
BEGIN SELECT RAISE(ABORT, 'feedback job payload is immutable'); END;
CREATE TRIGGER IF NOT EXISTS feedback_jobs_state_machine BEFORE UPDATE OF state ON feedback_jobs WHEN NOT (
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
BEGIN SELECT RAISE(ABORT, 'invalid feedback job state transition'); END;
CREATE TRIGGER IF NOT EXISTS feedback_jobs_lease_guard
BEFORE UPDATE OF owner,lease_expires_at,heartbeat_at ON feedback_jobs
WHEN OLD.state='RUNNING' AND NEW.state='RUNNING' AND (
  NEW.owner!=OLD.owner OR NEW.lease_expires_at IS NULL OR NEW.heartbeat_at IS NULL)
BEGIN SELECT RAISE(ABORT, 'invalid feedback job lease update'); END;
CREATE TRIGGER IF NOT EXISTS feedback_events_insert_guard BEFORE INSERT ON feedback_events
WHEN NOT EXISTS (SELECT 1 FROM feedback_jobs WHERE id=NEW.job_id
  AND kind='record_feedback' AND state='RUNNING')
BEGIN SELECT RAISE(ABORT, 'feedback event requires running job'); END;
CREATE TRIGGER IF NOT EXISTS feedback_events_immutable_update BEFORE UPDATE ON feedback_events
BEGIN SELECT RAISE(ABORT, 'feedback events are append-only'); END;
CREATE TRIGGER IF NOT EXISTS feedback_events_immutable_delete BEFORE DELETE ON feedback_events
BEGIN SELECT RAISE(ABORT, 'feedback events are append-only'); END;
CREATE TRIGGER IF NOT EXISTS global_sync_immutable_update BEFORE UPDATE ON global_sync
BEGIN SELECT RAISE(ABORT, 'global sync links are immutable'); END;
CREATE TRIGGER IF NOT EXISTS global_sync_immutable_delete BEFORE DELETE ON global_sync
BEGIN SELECT RAISE(ABORT, 'global sync links are immutable'); END;
CREATE TRIGGER IF NOT EXISTS global_sync_insert_guard BEFORE INSERT ON global_sync WHEN
  NOT EXISTS (SELECT 1 FROM documents source JOIN projects project
    ON project.id=source.project_id WHERE source.id=NEW.source_document_id
    AND project.scope!='global') OR NOT EXISTS (
    SELECT 1 FROM documents target JOIN projects project
    ON project.id=target.project_id WHERE target.id=NEW.global_document_id
    AND project.scope='global' AND project.alias='global-knowledge')
BEGIN SELECT RAISE(ABORT, 'global sync scope is invalid'); END;
`)
