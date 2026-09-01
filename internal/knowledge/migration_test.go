package knowledge

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const legacyVersionsStateMachine = `CREATE TRIGGER versions_state_machine BEFORE INSERT ON versions
WHEN NEW.ordinal != COALESCE((SELECT MAX(ordinal) + 1 FROM versions
  WHERE document_id = NEW.document_id), 1) OR NOT (
  (NEW.ordinal = 1 AND NEW.state = 'candidate') OR EXISTS (
    SELECT 1 FROM versions prior WHERE prior.document_id = NEW.document_id
    AND prior.ordinal = NEW.ordinal - 1 AND (
      (prior.state = 'candidate' AND NEW.state IN ('candidate','approved','verified','tombstone')) OR
      (prior.state = 'approved' AND NEW.state IN ('verified','tombstone')) OR
      (prior.state = 'verified' AND NEW.state = 'tombstone'))))
BEGIN SELECT RAISE(ABORT, 'invalid governance state transition'); END`

func TestLegacyV3MigrationPreservesJobsObjectsAndData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite3")
	database := openLegacyV3Database(t, path)
	wantJobsSQL := objectSQL(t, database, "table", jobsTable)
	wantIndexSQL := objectSQL(t, database, "index", "legacy_jobs_payload_idx")
	wantKnownTriggerSQL := objectSQL(t, database, "trigger", "jobs_insert_guard")
	wantUnknownTriggerSQL := objectSQL(t, database, "trigger", "legacy_jobs_update_audit")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	service, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if service.feedbackJobs != feedbackJobsTable {
		t.Fatalf("feedback queue table = %q", service.feedbackJobs)
	}
	if version, err := service.SchemaVersion(); err != nil || version != LatestSchema {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	if got := objectSQL(t, service.database, "table", jobsTable); got != wantJobsSQL {
		t.Fatalf("jobs SQL changed during migration:\nwant %s\n got %s", wantJobsSQL, got)
	}
	if strings.Contains(wantJobsSQL, "record_feedback") {
		t.Fatalf("legacy jobs fixture unexpectedly accepted feedback: %s", wantJobsSQL)
	}
	if got := objectSQL(t, service.database, "index", "legacy_jobs_payload_idx"); got != wantIndexSQL {
		t.Fatalf("legacy index changed: %q != %q", got, wantIndexSQL)
	}
	if got := objectSQL(t, service.database, "trigger", "jobs_insert_guard"); got != wantKnownTriggerSQL {
		t.Fatalf("known jobs trigger changed: %q != %q", got, wantKnownTriggerSQL)
	}
	if got := objectSQL(t, service.database, "trigger", "legacy_jobs_update_audit"); got != wantUnknownTriggerSQL {
		t.Fatalf("unknown jobs trigger changed: %q != %q", got, wantUnknownTriggerSQL)
	}
	var payload, state, marker string
	if err := service.database.QueryRow(
		"SELECT payload,state,legacy_marker FROM jobs WHERE dedupe_key='legacy-key'",
	).Scan(&payload, &state, &marker); err != nil {
		t.Fatal(err)
	}
	if payload != `{"legacy":true}` || state != "QUEUED" || marker != "keep-me" {
		t.Fatalf("legacy job changed: payload=%q state=%q marker=%q", payload, state, marker)
	}
	var migrationUpdates int
	if err := service.database.QueryRow("SELECT COUNT(*) FROM legacy_job_events").Scan(&migrationUpdates); err != nil {
		t.Fatal(err)
	}
	if migrationUpdates != 0 {
		t.Fatalf("migration updated legacy jobs %d times", migrationUpdates)
	}
	columns, err := tableColumns(service.database, "jobs")
	if err != nil || !columns["legacy_marker"] {
		t.Fatalf("legacy jobs column missing: %#v, %v", columns, err)
	}
	provenanceColumns, err := tableColumns(service.database, "import_provenance")
	if err != nil || !provenanceColumns["governance_state"] {
		t.Fatalf("legacy provenance was not expanded: %#v, %v", provenanceColumns, err)
	}

	projectID, err := service.CreateProject("project", "legacy-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	documentID, err := service.CreateCandidate(projectID, "legacy", "migrated", "test")
	if err != nil {
		t.Fatal(err)
	}
	invocations := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
	}
	for _, invocation := range invocations[:3] {
		if err := service.RecordFeedback(documentID, invocation, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.RecordFeedback(documentID, invocations[3], false); !errors.Is(err, errLegacyFeedbackDowngrade) {
		t.Fatalf("legacy feedback downgrade error = %v", err)
	}
	assertFeedback(t, service, documentID, 3, "verified")
	var feedbackJobs, baseFeedbackJobs int
	if err := service.database.QueryRow("SELECT COUNT(*) FROM feedback_jobs WHERE kind='record_feedback'").Scan(&feedbackJobs); err != nil {
		t.Fatal(err)
	}
	if err := service.database.QueryRow("SELECT COUNT(*) FROM jobs WHERE kind='record_feedback'").Scan(&baseFeedbackJobs); err != nil {
		t.Fatal(err)
	}
	if feedbackJobs != 4 || baseFeedbackJobs != 0 {
		t.Fatalf("feedback routing: extension=%d base=%d", feedbackJobs, baseFeedbackJobs)
	}
}

func TestMigrationStepsRollbackUserVersionAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollback.sqlite3")
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE VIEW snapshots AS SELECT 1 AS id"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if service, err := Open(path); err == nil {
		service.Close()
		t.Fatal("migration with a v2 object collision succeeded")
	}

	database, err = sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version, projects int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='projects'",
	).Scan(&projects); err != nil {
		t.Fatal(err)
	}
	if version != 0 || projects != 0 {
		t.Fatalf("partial migration survived rollback: version=%d projects=%d", version, projects)
	}
	if got := objectSQL(t, database, "view", "snapshots"); got == "" {
		t.Fatal("pre-existing view was lost during rollback")
	}
}

func TestMigrationAndLeaseRecoveryRollbackTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease-rollback.sqlite3")
	database := openLegacyV3Database(t, path)
	if _, err := database.Exec(`UPDATE jobs SET state='RUNNING',owner='legacy-owner',
heartbeat_at='invalid-heartbeat',lease_expires_at='invalid-lease',attempt=attempt+1
WHERE dedupe_key='legacy-key'`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if service, err := Open(path); err == nil {
		service.Close()
		t.Fatal("migration with an invalid RUNNING lease succeeded")
	}

	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version, feedbackTables int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='feedback_jobs'",
	).Scan(&feedbackTables); err != nil {
		t.Fatal(err)
	}
	columns, err := tableColumns(database, "import_provenance")
	if err != nil {
		t.Fatal(err)
	}
	if version != 3 || feedbackTables != 0 || columns["governance_state"] {
		t.Fatalf("failed recovery committed migration: version=%d feedback=%d columns=%#v", version, feedbackTables, columns)
	}
}

func TestOpenRecoversExpiredAndIncompleteLeasesAcrossJobTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease-recovery.sqlite3")
	service, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		table      string
		kind       string
		id         int64
		incomplete bool
	}{
		{table: jobsTable, kind: "create_project"},
		{table: feedbackJobsTable, kind: "record_feedback", incomplete: true},
	}
	for index := range tests {
		item := &tests[index]
		result, err := service.database.Exec(
			"INSERT INTO "+item.table+"(kind,payload,state,dedupe_key,created_at) VALUES (?, '{}', 'QUEUED', ?, 'created')",
			item.kind,
			"stale-"+item.table,
		)
		if err != nil {
			service.Close()
			t.Fatal(err)
		}
		item.id, err = result.LastInsertId()
		if err != nil {
			service.Close()
			t.Fatal(err)
		}
		if _, err := service.database.Exec(
			"UPDATE "+item.table+" SET state='RUNNING',owner='stale-owner',heartbeat_at='2000-01-01T00:00:00Z',lease_expires_at='2000-01-01T00:00:01Z',started_at='started',attempt=attempt+1 WHERE id=?",
			item.id,
		); err != nil {
			service.Close()
			t.Fatal(err)
		}
		if item.incomplete {
			if _, err := service.database.Exec("DROP TRIGGER " + item.table + "_lease_guard"); err != nil {
				service.Close()
				t.Fatal(err)
			}
			if _, err := service.database.Exec(
				"UPDATE "+item.table+" SET owner=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=?",
				item.id,
			); err != nil {
				service.Close()
				t.Fatal(err)
			}
		}
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	service, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, item := range tests {
		t.Run(item.table, func(t *testing.T) {
			var state string
			var owner, heartbeat, lease, started sql.NullString
			var attempt int
			if err := service.database.QueryRow(
				"SELECT state,owner,heartbeat_at,lease_expires_at,started_at,attempt FROM "+item.table+" WHERE id=?",
				item.id,
			).Scan(&state, &owner, &heartbeat, &lease, &started, &attempt); err != nil {
				t.Fatal(err)
			}
			if state != "QUEUED" || owner.Valid || heartbeat.Valid || lease.Valid {
				t.Fatalf("recovered lease = state:%s owner:%v heartbeat:%v lease:%v", state, owner, heartbeat, lease)
			}
			if !started.Valid || started.String != "started" || attempt != 1 {
				t.Fatalf("recovery changed job history: started=%v attempt=%d", started, attempt)
			}
		})
	}
}

func openLegacyV3Database(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range schemaV1Statements {
		if strings.Contains(statement, "versions_state_machine") {
			continue
		}
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("create legacy v1 schema: %v", err)
		}
	}
	if _, err := database.Exec(legacyVersionsStateMachine); err != nil {
		database.Close()
		t.Fatal(err)
	}
	legacyProvenance := `CREATE TABLE import_provenance (
document_id TEXT NOT NULL REFERENCES documents(id),source_kind TEXT NOT NULL,source_ref TEXT NOT NULL,
source_sha256 TEXT NOT NULL,scanner TEXT NOT NULL,scan_state TEXT NOT NULL CHECK(scan_state='CLEAN'),
PRIMARY KEY(document_id,source_kind,source_ref))`
	statements := []string{
		legacyProvenance,
		"ALTER TABLE jobs ADD COLUMN legacy_marker TEXT NOT NULL DEFAULT 'default-marker'",
		"CREATE INDEX legacy_jobs_payload_idx ON jobs(payload)",
		"CREATE TABLE legacy_job_events(job_id INTEGER NOT NULL,state TEXT NOT NULL)",
		`INSERT INTO jobs(kind,payload,state,dedupe_key,created_at,legacy_marker)
VALUES ('create_project','{"legacy":true}','QUEUED','legacy-key','legacy','keep-me')`,
		`CREATE TRIGGER legacy_jobs_update_audit AFTER UPDATE ON jobs
BEGIN INSERT INTO legacy_job_events(job_id,state) VALUES (NEW.id,NEW.state); END`,
		"PRAGMA user_version=3",
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("create legacy v3 fixture: %v", err)
		}
	}
	return database
}

func objectSQL(t *testing.T, database *sql.DB, kind, name string) string {
	t.Helper()
	var statement string
	if err := database.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type=? AND name=?",
		kind,
		name,
	).Scan(&statement); err != nil {
		t.Fatalf("read %s %s SQL: %v", kind, name, err)
	}
	return statement
}
