package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const LatestSchema = 4

const jobsNextSchema = `CREATE TABLE jobs_next (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL CHECK(kind IN ('create_project','create_candidate','edit_candidate','soft_delete_candidate','append_state','create_snapshot','record_feedback')),
  payload TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('QUEUED','RUNNING','SUCCEEDED','FAILED')),
  dedupe_key TEXT UNIQUE, error TEXT, created_at TEXT NOT NULL, started_at TEXT,
  finished_at TEXT, owner TEXT, lease_expires_at TEXT, heartbeat_at TEXT,
  attempt INTEGER NOT NULL DEFAULT 0
)`

var knownTriggerNames = []string{
	"projects_immutable", "documents_soft_delete_candidate", "originals_immutable_update",
	"originals_immutable_delete", "versions_append_only", "versions_no_delete",
	"versions_state_machine", "audit_immutable_update", "audit_immutable_delete",
	"jobs_insert_guard", "jobs_payload_immutable", "jobs_state_machine", "jobs_lease_guard",
	"snapshots_insert_guard", "snapshots_immutable", "snapshots_no_delete",
	"snapshot_versions_insert_guard", "snapshot_versions_immutable", "snapshot_versions_no_delete",
	"active_snapshots_insert_guard", "active_snapshots_update_guard", "active_snapshots_no_delete",
	"feedback_events_insert_guard", "feedback_events_immutable_update", "feedback_events_immutable_delete",
	"global_sync_immutable_update", "global_sync_immutable_delete", "global_sync_insert_guard",
}

func openDatabase(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, err
	}
	dsn := "file:" + url.PathEscape(filepath.ToSlash(absolute)) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_txlock=immediate"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		return nil, err
	}
	if err := migrate(database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func migrate(database *sql.DB) error {
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > LatestSchema {
		return fmt.Errorf("knowledge schema %d is newer than supported schema %d", version, LatestSchema)
	}
	connection, err := database.Conn(context.Background())
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := prepareExistingSchema(connection); err != nil {
		return err
	}
	for _, statement := range schemaStatements {
		if _, err := connection.ExecContext(context.Background(), statement); err != nil {
			return fmt.Errorf("knowledge migration: %w", err)
		}
	}
	if _, err := connection.ExecContext(context.Background(), "PRAGMA user_version = "+strconv.Itoa(LatestSchema)); err != nil {
		return err
	}
	if _, err := connection.ExecContext(context.Background(), "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

func prepareExistingSchema(connection *sql.Conn) error {
	ctx := context.Background()
	exists, err := objectExists(connection, "table", "jobs")
	if err != nil {
		return err
	}
	for _, name := range knownTriggerNames {
		if _, err := connection.ExecContext(ctx, "DROP TRIGGER IF EXISTS "+name); err != nil {
			return fmt.Errorf("drop knowledge trigger %s: %w", name, err)
		}
	}
	if !exists {
		return repairLegacyImportSchema(connection)
	}
	columns, err := connectionColumns(connection, "jobs")
	if err != nil {
		return err
	}
	optional := []struct{ name, definition string }{
		{"dedupe_key", "TEXT"}, {"error", "TEXT"}, {"started_at", "TEXT"},
		{"finished_at", "TEXT"}, {"owner", "TEXT"}, {"lease_expires_at", "TEXT"},
		{"heartbeat_at", "TEXT"}, {"attempt", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, item := range optional {
		if !columns[item.name] {
			if _, err := connection.ExecContext(ctx, "ALTER TABLE jobs ADD COLUMN "+item.name+" "+item.definition); err != nil {
				return fmt.Errorf("repair jobs.%s: %w", item.name, err)
			}
		}
	}
	if err := recoverRunningJobs(connection); err != nil {
		return err
	}
	var tableSQL string
	if err := connection.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='jobs'").Scan(&tableSQL); err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(tableSQL), "record_feedback") {
		return repairLegacyImportSchema(connection)
	}
	staging, err := objectExists(connection, "table", "jobs_next")
	if err != nil {
		return err
	}
	if staging {
		return errors.New("knowledge migration found a stale jobs_next table")
	}
	if _, err := connection.ExecContext(ctx, jobsNextSchema); err != nil {
		return fmt.Errorf("create jobs migration table: %w", err)
	}
	columnsCSV := "id,kind,payload,state,dedupe_key,error,created_at,started_at,finished_at,owner,lease_expires_at,heartbeat_at,attempt"
	if _, err := connection.ExecContext(ctx, "INSERT INTO jobs_next("+columnsCSV+") SELECT "+columnsCSV+" FROM jobs"); err != nil {
		return fmt.Errorf("copy jobs during schema v4 migration: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "DROP TABLE jobs"); err != nil {
		return fmt.Errorf("replace jobs during schema v4 migration: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "ALTER TABLE jobs_next RENAME TO jobs"); err != nil {
		return fmt.Errorf("finish jobs schema v4 migration: %w", err)
	}
	return repairLegacyImportSchema(connection)
}

func repairLegacyImportSchema(connection *sql.Conn) error {
	exists, err := objectExists(connection, "table", "import_provenance")
	if err != nil || !exists {
		return err
	}
	columns, err := connectionColumns(connection, "import_provenance")
	if err != nil {
		return err
	}
	if columns["governance_state"] {
		return nil
	}
	_, err = connection.ExecContext(context.Background(), `ALTER TABLE import_provenance
ADD COLUMN governance_state TEXT NOT NULL DEFAULT 'CANDIDATE' CHECK(governance_state='CANDIDATE')`)
	if err != nil {
		return fmt.Errorf("repair legacy import provenance: %w", err)
	}
	return nil
}

func recoverRunningJobs(connection *sql.Conn) error {
	ctx := context.Background()
	rows, err := connection.QueryContext(ctx, "SELECT id,owner,heartbeat_at,lease_expires_at FROM jobs WHERE state='RUNNING' ORDER BY id")
	if err != nil {
		return err
	}
	type runningJob struct {
		id                      int64
		owner, heartbeat, lease sql.NullString
	}
	var jobs []runningJob
	for rows.Next() {
		var job runningJob
		if err := rows.Scan(&job.id, &job.owner, &job.heartbeat, &job.lease); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC()
	var recoverable []int64
	live := 0
	for _, job := range jobs {
		if !job.owner.Valid || !job.heartbeat.Valid || !job.lease.Valid {
			recoverable = append(recoverable, job.id)
			continue
		}
		lease, err := time.Parse(time.RFC3339Nano, job.lease.String)
		if err != nil {
			return fmt.Errorf("job %d has an invalid RUNNING lease", job.id)
		}
		if !lease.After(now) {
			recoverable = append(recoverable, job.id)
		} else {
			live++
		}
	}
	if live > 1 {
		return errors.New("knowledge database has multiple live RUNNING job leases")
	}
	for _, identifier := range recoverable {
		if _, err := connection.ExecContext(ctx, `UPDATE jobs SET state='QUEUED',owner=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=?`, identifier); err != nil {
			return fmt.Errorf("recover expired job %d: %w", identifier, err)
		}
	}
	return nil
}

func objectExists(connection *sql.Conn, kind, name string) (bool, error) {
	var count int
	err := connection.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?", kind, name).Scan(&count)
	return count != 0, err
}

func connectionColumns(connection *sql.Conn, table string) (map[string]bool, error) {
	if !validTable(table) {
		return nil, errors.New("unsupported table")
	}
	rows, err := connection.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

func tableColumns(database *sql.DB, table string) (map[string]bool, error) {
	if !validTable(table) {
		return nil, errors.New("unsupported table")
	}
	rows, err := database.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func validTable(table string) bool {
	for _, value := range []string{
		"projects", "originals", "documents", "versions", "chunks", "sources", "governance", "audit", "jobs",
		"snapshots", "snapshot_versions", "active_snapshots", "import_documents", "import_provenance", "import_receipts",
		"feedback_events", "global_sync",
	} {
		if table == value {
			return true
		}
	}
	return false
}

var schemaStatements = splitStatements(`
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
  kind TEXT NOT NULL CHECK(kind IN ('create_project','create_candidate','edit_candidate','soft_delete_candidate','append_state','create_snapshot','record_feedback')),
  payload TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('QUEUED','RUNNING','SUCCEEDED','FAILED')),
  dedupe_key TEXT UNIQUE, error TEXT, created_at TEXT NOT NULL, started_at TEXT,
  finished_at TEXT, owner TEXT, lease_expires_at TEXT, heartbeat_at TEXT,
  attempt INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS snapshots (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  generation INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('BUILDING','ACTIVE')),
  created_at TEXT NOT NULL, UNIQUE(project_id, generation)
);
CREATE TABLE IF NOT EXISTS snapshot_versions (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id),
  document_id TEXT NOT NULL REFERENCES documents(id),
  version_id INTEGER NOT NULL REFERENCES versions(id),
  PRIMARY KEY(snapshot_id, document_id)
);
CREATE TABLE IF NOT EXISTS active_snapshots (
  project_id TEXT PRIMARY KEY REFERENCES projects(id),
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id)
);
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
CREATE TABLE IF NOT EXISTS feedback_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  job_id INTEGER NOT NULL UNIQUE REFERENCES jobs(id),
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
    AND NEW.attempt=OLD.attempt AND julianday(OLD.lease_expires_at)<=julianday('now')))
BEGIN SELECT RAISE(ABORT, 'invalid job state transition'); END;
CREATE TRIGGER IF NOT EXISTS jobs_lease_guard BEFORE UPDATE OF owner,lease_expires_at,heartbeat_at ON jobs
WHEN OLD.state='RUNNING' AND NEW.state='RUNNING' AND (
  NEW.owner!=OLD.owner OR NEW.lease_expires_at IS NULL OR NEW.heartbeat_at IS NULL)
BEGIN SELECT RAISE(ABORT, 'invalid job lease update'); END;
CREATE TRIGGER IF NOT EXISTS snapshots_insert_guard BEFORE INSERT ON snapshots
WHEN NEW.state!='BUILDING'
BEGIN SELECT RAISE(ABORT, 'snapshots begin building'); END;
CREATE TRIGGER IF NOT EXISTS snapshots_immutable BEFORE UPDATE ON snapshots WHEN NOT (
  OLD.state='BUILDING' AND NEW.state='ACTIVE' AND NEW.id=OLD.id
  AND NEW.project_id=OLD.project_id AND NEW.generation=OLD.generation
  AND NEW.created_at=OLD.created_at)
BEGIN SELECT RAISE(ABORT, 'snapshots are immutable'); END;
CREATE TRIGGER IF NOT EXISTS snapshots_no_delete BEFORE DELETE ON snapshots
BEGIN SELECT RAISE(ABORT, 'snapshots are immutable'); END;
CREATE TRIGGER IF NOT EXISTS snapshot_versions_insert_guard BEFORE INSERT ON snapshot_versions
WHEN NOT EXISTS (SELECT 1 FROM snapshots s JOIN documents d ON d.id=NEW.document_id
  JOIN versions v ON v.id=NEW.version_id AND v.document_id=d.id
  WHERE s.id=NEW.snapshot_id AND s.project_id=d.project_id AND s.state='BUILDING')
BEGIN SELECT RAISE(ABORT, 'snapshot membership requires building snapshot'); END;
CREATE TRIGGER IF NOT EXISTS snapshot_versions_immutable BEFORE UPDATE ON snapshot_versions
BEGIN SELECT RAISE(ABORT, 'snapshot membership is immutable'); END;
CREATE TRIGGER IF NOT EXISTS snapshot_versions_no_delete BEFORE DELETE ON snapshot_versions
BEGIN SELECT RAISE(ABORT, 'snapshot membership is immutable'); END;
CREATE TRIGGER IF NOT EXISTS active_snapshots_insert_guard BEFORE INSERT ON active_snapshots
WHEN NOT EXISTS (SELECT 1 FROM snapshots WHERE id=NEW.snapshot_id
  AND project_id=NEW.project_id AND state='ACTIVE')
BEGIN SELECT RAISE(ABORT, 'active snapshot must be active generation'); END;
CREATE TRIGGER IF NOT EXISTS active_snapshots_update_guard BEFORE UPDATE ON active_snapshots
WHEN NOT EXISTS (SELECT 1 FROM snapshots WHERE id=NEW.snapshot_id
  AND project_id=NEW.project_id AND state='ACTIVE')
BEGIN SELECT RAISE(ABORT, 'active snapshot must be active generation'); END;
CREATE TRIGGER IF NOT EXISTS active_snapshots_no_delete BEFORE DELETE ON active_snapshots
BEGIN SELECT RAISE(ABORT, 'active snapshot pointer is immutable'); END;
CREATE TRIGGER IF NOT EXISTS feedback_events_insert_guard BEFORE INSERT ON feedback_events
WHEN NOT EXISTS (SELECT 1 FROM jobs WHERE id=NEW.job_id
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

func splitStatements(source string) []string {
	// Trigger bodies contain semicolons. Split only after END; or ordinary
	// statement terminators, retaining compact migration source.
	var result []string
	var current strings.Builder
	inTrigger := false
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(trimmed), "CREATE TRIGGER") {
			inTrigger = true
		}
		current.WriteString(line)
		current.WriteByte('\n')
		if (!inTrigger && strings.HasSuffix(trimmed, ";")) || (inTrigger && strings.EqualFold(trimmed, "END;")) {
			statement := strings.TrimSpace(current.String())
			statement = strings.TrimSuffix(statement, ";")
			result = append(result, statement)
			current.Reset()
			inTrigger = false
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		result = append(result, strings.TrimSpace(current.String()))
	}
	return result
}
