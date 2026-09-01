package knowledge

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoricalV4FeedbackQueueRemainsOnJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical-v4.sqlite3")
	database := openHistoricalV4Database(t, path)
	objects := map[string]string{
		"table/jobs":                           objectSQL(t, database, "table", jobsTable),
		"table/feedback_events":                objectSQL(t, database, "table", "feedback_events"),
		"trigger/jobs_insert_guard":            objectSQL(t, database, "trigger", "jobs_insert_guard"),
		"trigger/feedback_events_insert_guard": objectSQL(t, database, "trigger", "feedback_events_insert_guard"),
		"index/historical_jobs_payload_idx":    objectSQL(t, database, "index", "historical_jobs_payload_idx"),
		"trigger/historical_jobs_update_audit": objectSQL(t, database, "trigger", "historical_jobs_update_audit"),
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	service, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if service.feedbackJobs != jobsTable {
		t.Fatalf("feedback queue table = %q", service.feedbackJobs)
	}
	for key, want := range objects {
		parts := strings.SplitN(key, "/", 2)
		if got := objectSQL(t, service.database, parts[0], parts[1]); got != want {
			t.Fatalf("historical %s changed:\nwant %s\n got %s", key, want, got)
		}
	}
	var payload, marker string
	if err := service.database.QueryRow(
		"SELECT payload,historical_marker FROM jobs WHERE dedupe_key='historical-key'",
	).Scan(&payload, &marker); err != nil {
		t.Fatal(err)
	}
	if payload != `{"historical":true}` || marker != "keep-me" {
		t.Fatalf("historical job changed: payload=%q marker=%q", payload, marker)
	}
	var migrationUpdates int
	if err := service.database.QueryRow("SELECT COUNT(*) FROM historical_job_events").Scan(&migrationUpdates); err != nil {
		t.Fatal(err)
	}
	if migrationUpdates != 0 {
		t.Fatalf("open updated historical jobs %d times", migrationUpdates)
	}

	projectID, err := service.CreateProject("project", "historical-v4")
	if err != nil {
		t.Fatal(err)
	}
	documentID, err := service.CreateCandidate(projectID, "historical", "feedback", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordFeedback(documentID, "11111111-1111-4111-8111-111111111111", true); err != nil {
		t.Fatal(err)
	}
	var jobID, eventJobID int64
	if err := service.database.QueryRow(
		"SELECT id FROM jobs WHERE kind='record_feedback' ORDER BY id DESC LIMIT 1",
	).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if err := service.database.QueryRow(
		"SELECT job_id FROM feedback_events WHERE document_id=?",
		documentID,
	).Scan(&eventJobID); err != nil {
		t.Fatal(err)
	}
	if jobID <= 0 || eventJobID != jobID {
		t.Fatalf("historical feedback job identifiers: job=%d event=%d", jobID, eventJobID)
	}
	job, err := service.Job(jobID)
	if err != nil || job.ID != jobID || job.State != "SUCCEEDED" {
		t.Fatalf("historical feedback job = %#v, %v", job, err)
	}
	var extensionJobs int
	if err := service.database.QueryRow("SELECT COUNT(*) FROM feedback_jobs").Scan(&extensionJobs); err != nil {
		t.Fatal(err)
	}
	if extensionJobs != 0 {
		t.Fatalf("historical feedback unexpectedly routed to extension queue: %d", extensionJobs)
	}
}

func TestHistoricalV4MissingFeedbackGuardUsesForeignKeyRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical-v4-missing-guard.sqlite3")
	database := openHistoricalV4Database(t, path)
	if _, err := database.Exec("DROP TRIGGER feedback_events_insert_guard"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	service, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if service.feedbackJobs != jobsTable {
		t.Fatalf("feedback queue table = %q", service.feedbackJobs)
	}
	guard := strings.ToLower(strings.Join(strings.Fields(
		objectSQL(t, service.database, "trigger", "feedback_events_insert_guard"),
	), " "))
	if !strings.Contains(guard, "from jobs where") {
		t.Fatalf("feedback guard does not use historical jobs route: %s", guard)
	}
	projectID, err := service.CreateProject("project", "missing-guard")
	if err != nil {
		t.Fatal(err)
	}
	documentID, err := service.CreateCandidate(projectID, "historical", "feedback", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordFeedback(documentID, "55555555-5555-4555-8555-555555555555", true); err != nil {
		t.Fatal(err)
	}
}

func TestHistoricalV4EquivalentFeedbackGuardIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical-v4-equivalent-guard.sqlite3")
	database := openHistoricalV4Database(t, path)
	if _, err := database.Exec("DROP TRIGGER feedback_events_insert_guard"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	statement := `CREATE TRIGGER feedback_events_insert_guard
BEFORE INSERT ON feedback_events WHEN NOT EXISTS (
  SELECT 1 FROM jobs WHERE id = NEW.job_id
  AND kind = 'record_feedback' AND state = 'RUNNING')
BEGIN SELECT RAISE(ABORT, 'historical running feedback job required'); END`
	if _, err := database.Exec(statement); err != nil {
		database.Close()
		t.Fatal(err)
	}
	want := objectSQL(t, database, "trigger", "feedback_events_insert_guard")
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	service, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if got := objectSQL(t, service.database, "trigger", "feedback_events_insert_guard"); got != want {
		t.Fatalf("semantically valid historical guard changed:\nwant %s\n got %s", want, got)
	}
}

func TestHistoricalV4FeedbackGuardMissingKindIsRepaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical-v4-missing-kind-guard.sqlite3")
	database := openHistoricalV4Database(t, path)
	if _, err := database.Exec("DROP TRIGGER feedback_events_insert_guard"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	missingKind := `CREATE TRIGGER feedback_events_insert_guard
BEFORE INSERT ON feedback_events WHEN NOT EXISTS (
  SELECT 1 FROM jobs WHERE id=NEW.job_id AND state='RUNNING')
BEGIN SELECT RAISE(ABORT, 'historical running job required'); END`
	if _, err := database.Exec(missingKind); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	service, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if got := objectSQL(t, service.database, "trigger", "feedback_events_insert_guard"); got == missingKind {
		t.Fatal("historical feedback guard without a kind constraint was preserved")
	}
}

func TestHistoricalV4InvertedFeedbackGuardIsRepaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical-v4-inverted-guard.sqlite3")
	database := openHistoricalV4Database(t, path)
	if _, err := database.Exec("DROP TRIGGER feedback_events_insert_guard"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	inverted := `CREATE TRIGGER feedback_events_insert_guard
BEFORE INSERT ON feedback_events WHEN EXISTS (
  SELECT 1 FROM jobs WHERE id=NEW.job_id AND kind='record_feedback' AND state='RUNNING')
BEGIN SELECT RAISE(ABORT, 'inverted historical guard'); END`
	if _, err := database.Exec(inverted); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	service, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if got := objectSQL(t, service.database, "trigger", "feedback_events_insert_guard"); got == inverted {
		t.Fatal("semantically inverted historical feedback guard was preserved")
	}
}

func TestFeedbackRouteValidationRollsBackMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-feedback-route.sqlite3")
	database := openLegacyV3Database(t, path)
	if _, err := database.Exec(`CREATE TABLE feedback_events (
id INTEGER PRIMARY KEY AUTOINCREMENT,
job_id INTEGER NOT NULL UNIQUE REFERENCES jobs(id))`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	if service, err := Open(path); err == nil {
		service.Close()
		t.Fatal("migration accepted a feedback route whose job table rejects record_feedback")
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version, feedbackJobs, guard int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='feedback_jobs'",
	).Scan(&feedbackJobs); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name='feedback_events_insert_guard'",
	).Scan(&guard); err != nil {
		t.Fatal(err)
	}
	if version != 3 || feedbackJobs != 0 || guard != 0 {
		t.Fatalf("failed route validation committed migration: version=%d feedback_jobs=%d guard=%d", version, feedbackJobs, guard)
	}
}

func openHistoricalV4Database(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range [][]string{schemaV1Statements, schemaV2Statements, schemaV3Statements} {
		for _, statement := range group {
			if strings.HasPrefix(statement, "CREATE TABLE IF NOT EXISTS jobs (") {
				statement = strings.Replace(
					statement,
					"'append_state','create_snapshot'))",
					"'append_state','create_snapshot','record_feedback'))",
					1,
				)
			}
			if _, err := database.Exec(statement); err != nil {
				database.Close()
				t.Fatalf("create historical v4 base schema: %v", err)
			}
		}
	}
	for _, statement := range schemaV4Statements {
		if strings.HasPrefix(statement, "CREATE TABLE IF NOT EXISTS feedback_jobs (") ||
			strings.HasPrefix(statement, "CREATE TRIGGER IF NOT EXISTS feedback_jobs_") {
			continue
		}
		statement = strings.ReplaceAll(statement, feedbackJobsTable, jobsTable)
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("create historical v4 feedback schema: %v", err)
		}
	}
	statements := []string{
		"ALTER TABLE jobs ADD COLUMN historical_marker TEXT NOT NULL DEFAULT 'default-marker'",
		"CREATE INDEX historical_jobs_payload_idx ON jobs(payload)",
		"CREATE TABLE historical_job_events(job_id INTEGER NOT NULL,state TEXT NOT NULL)",
		`INSERT INTO jobs(kind,payload,state,dedupe_key,created_at,historical_marker)
VALUES ('create_project','{"historical":true}','QUEUED','historical-key','historical','keep-me')`,
		`CREATE TRIGGER historical_jobs_update_audit AFTER UPDATE ON jobs
BEGIN INSERT INTO historical_job_events(job_id,state) VALUES (NEW.id,NEW.state); END`,
		"PRAGMA user_version=4",
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("create historical v4 fixture: %v", err)
		}
	}
	return database
}
