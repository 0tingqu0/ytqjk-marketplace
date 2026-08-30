package knowledge

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceLifecycle(t *testing.T) {
	service, err := Open(filepath.Join(t.TempDir(), "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	version, err := service.SchemaVersion()
	if err != nil || version != LatestSchema {
		t.Fatalf("schema version = %d, %v", version, err)
	}
	projectID, err := service.CreateProject("project", "example")
	if err != nil {
		t.Fatal(err)
	}
	duplicateID, err := service.CreateProject("project", "example")
	if err != nil || duplicateID != projectID {
		t.Fatalf("idempotent project = %q, %v", duplicateID, err)
	}
	documentID, err := service.CreateCandidate(projectID, "Go migration", "The runtime is implemented in Go.", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.EditCandidate(documentID, "The entire local runtime is implemented in Go.", "test-edit"); err != nil {
		t.Fatal(err)
	}
	if err := service.AppendState(documentID, "APPROVED", nil); err != nil {
		t.Fatal(err)
	}
	snapshotID, err := service.CreateSnapshot(projectID)
	if err != nil || snapshotID == "" {
		t.Fatalf("snapshot = %q, %v", snapshotID, err)
	}
	results, err := service.Search(projectID, "runtime Go", 10)
	if err != nil || len(results) != 1 || results[0].DocumentID != documentID {
		t.Fatalf("search results = %#v, %v", results, err)
	}
	if err := service.RecordFeedback(documentID, "11111111-1111-4111-8111-111111111111", true); err != nil {
		t.Fatal(err)
	}
	feedback, err := service.FeedbackStatus(documentID)
	if err != nil || feedback["correct"] != true {
		t.Fatalf("feedback = %#v, %v", feedback, err)
	}
}

func TestFeedbackLifecycleAndGlobalMirror(t *testing.T) {
	service, err := Open(filepath.Join(t.TempDir(), "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	projectID, err := service.CreateProject("project", "feedback-project")
	if err != nil {
		t.Fatal(err)
	}
	documentID, err := service.CreateCandidate(projectID, "Feedback lifecycle", "candidate content", "test")
	if err != nil {
		t.Fatal(err)
	}
	invocations := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		"55555555-5555-4555-8555-555555555555",
		"66666666-6666-4666-8666-666666666666",
	}
	if err := service.RecordFeedback(documentID, invocations[0], true); err != nil {
		t.Fatal(err)
	}
	assertFeedback(t, service, documentID, 1, "candidate")
	if count, err := service.Count("global_sync"); err != nil || count != 0 {
		t.Fatalf("global sync after one approval = %d, %v", count, err)
	}
	if err := service.EditCandidate(documentID, "edited candidate content", "test-edit"); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordFeedback(documentID, invocations[1], true); err != nil {
		t.Fatal(err)
	}
	// Repeating an identical invocation is idempotent.
	if err := service.RecordFeedback(documentID, invocations[1], true); err != nil {
		t.Fatalf("idempotent feedback: %v", err)
	}
	if err := service.RecordFeedback(documentID, invocations[1], false); err == nil {
		t.Fatal("contradictory feedback invocation succeeded")
	}
	var failedJobs int
	if err := service.database.QueryRow("SELECT COUNT(*) FROM jobs WHERE kind='record_feedback' AND state='FAILED'").Scan(&failedJobs); err != nil || failedJobs != 1 {
		t.Fatalf("failed feedback jobs = %d, %v", failedJobs, err)
	}
	assertFeedback(t, service, documentID, 2, "approved")
	var globalID string
	if err := service.database.QueryRow("SELECT global_document_id FROM global_sync WHERE source_document_id=?", documentID).Scan(&globalID); err != nil {
		t.Fatal(err)
	}
	if !validUUID(globalID) {
		t.Fatalf("global mirror identifier = %q", globalID)
	}
	_, content, state, err := service.DocumentContent(globalID)
	if err != nil || content != "edited candidate content" || state != "approved" {
		t.Fatalf("global mirror = content %q state %q, %v", content, state, err)
	}
	if err := service.RecordFeedback(documentID, invocations[2], true); err != nil {
		t.Fatal(err)
	}
	assertFeedback(t, service, documentID, 3, "verified")
	if err := service.AppendState(globalID, "TOMBSTONE", nil); err == nil || !strings.Contains(err.Error(), "global mirrors") {
		t.Fatalf("global mirror mutation error = %v", err)
	}
	if err := service.RecordFeedback(documentID, invocations[3], false); err != nil {
		t.Fatal(err)
	}
	assertFeedback(t, service, documentID, 2, "approved")
	if err := service.RecordFeedback(documentID, invocations[4], false); err != nil {
		t.Fatal(err)
	}
	assertFeedback(t, service, documentID, 1, "candidate")
	if err := service.EditCandidate(documentID, "recycled content", "test-recycle"); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordFeedback(documentID, invocations[5], false); err != nil {
		t.Fatal(err)
	}
	assertFeedback(t, service, documentID, 0, "tombstone")
	_, content, state, err = service.DocumentContent(globalID)
	if err != nil || content != "recycled content" || state != "tombstone" {
		t.Fatalf("recycled global mirror = content %q state %q, %v", content, state, err)
	}
	recycled, err := service.RecycleBin(projectID)
	if err != nil || len(recycled) != 1 || recycled[0]["id"] != documentID {
		t.Fatalf("recycle bin = %#v, %v", recycled, err)
	}
	if _, err := service.database.Exec("UPDATE feedback_events SET score=3 WHERE document_id=?", documentID); err == nil {
		t.Fatal("feedback history was mutable")
	}
}

func TestMigratesLegacyV3JobsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite3")
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	legacy := `CREATE TABLE jobs (
id INTEGER PRIMARY KEY AUTOINCREMENT,
kind TEXT NOT NULL CHECK(kind IN ('create_project','create_candidate','edit_candidate','soft_delete_candidate','append_state','create_snapshot')),
payload TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('QUEUED','RUNNING','SUCCEEDED','FAILED')),
dedupe_key TEXT UNIQUE,error TEXT,created_at TEXT NOT NULL,started_at TEXT,finished_at TEXT,
owner TEXT,lease_expires_at TEXT,heartbeat_at TEXT,attempt INTEGER NOT NULL DEFAULT 0)`
	if _, err := database.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	legacyProvenance := `CREATE TABLE import_provenance (
document_id TEXT NOT NULL REFERENCES documents(id),source_kind TEXT NOT NULL,source_ref TEXT NOT NULL,
source_sha256 TEXT NOT NULL,scanner TEXT NOT NULL,scan_state TEXT NOT NULL CHECK(scan_state='CLEAN'),
PRIMARY KEY(document_id,source_kind,source_ref))`
	if _, err := database.Exec(legacyProvenance); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO jobs(kind,payload,state,created_at) VALUES ('create_project','{}','SUCCEEDED','legacy')"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA user_version=3"); err != nil {
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
	var tableSQL string
	if err := service.database.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='jobs'").Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableSQL, "record_feedback") {
		t.Fatalf("jobs table was not migrated: %s", tableSQL)
	}
	columns, err := tableColumns(service.database, "import_provenance")
	if err != nil || !columns["governance_state"] {
		t.Fatalf("legacy import provenance was not migrated: %#v, %v", columns, err)
	}
	if count, err := service.Count("jobs"); err != nil || count != 1 {
		t.Fatalf("migrated job count = %d, %v", count, err)
	}
	projectID, err := service.CreateProject("project", "legacy-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	documentID, err := service.CreateCandidate(projectID, "legacy", "migrated", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RecordFeedback(documentID, "77777777-7777-4777-8777-777777777777", true); err != nil {
		t.Fatal(err)
	}
}

func assertFeedback(t *testing.T, service *Service, documentID string, score int, state string) {
	t.Helper()
	status, err := service.FeedbackStatus(documentID)
	if err != nil {
		t.Fatal(err)
	}
	if status == nil || status["score"] != score || status["state"] != state {
		t.Fatalf("feedback status = %#v, want score=%d state=%s", status, score, state)
	}
}
