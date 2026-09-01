package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCreateProjectionRejectsPlaceholderV4Database(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "service", "knowledge.sqlite3")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	mutateProjectionDatabase(t, source,
		"CREATE TABLE placeholder(id INTEGER PRIMARY KEY)",
		"PRAGMA user_version=4",
	)
	request := ProjectionRequest{
		KnowledgeRoot:  root,
		SourcePath:     source,
		ProjectionRoot: filepath.Join(root, "handoffs", "projections"),
		OperationID:    "placeholder-v4",
	}
	_, err := CreateProjection(context.Background(), request)
	assertProjectionCode(t, err, ProjectionSourceInvalid)
	if _, statErr := os.Lstat(filepath.Join(request.ProjectionRoot, request.OperationID)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid source published projection: %v", statErr)
	}
}

func TestCreateProjectionRejectsIncompleteCanonicalSchema(t *testing.T) {
	tests := []struct {
		name       string
		statements []string
	}{
		{
			name:       "missing core column",
			statements: []string{"ALTER TABLE audit RENAME COLUMN detail TO legacy_detail"},
		},
		{
			name:       "missing core trigger",
			statements: []string{"DROP TRIGGER audit_immutable_update"},
		},
		{
			name: "weakened core trigger",
			statements: []string{
				"DROP TRIGGER versions_no_delete",
				`CREATE TRIGGER versions_no_delete BEFORE DELETE ON versions
				BEGIN SELECT 1; END`,
			},
		},
		{
			name: "feedback guard missing kind",
			statements: []string{
				"DROP TRIGGER feedback_events_insert_guard",
				`CREATE TRIGGER feedback_events_insert_guard BEFORE INSERT ON feedback_events
				WHEN NOT EXISTS (SELECT 1 FROM feedback_jobs WHERE id=NEW.job_id AND state='RUNNING')
				BEGIN SELECT RAISE(ABORT, 'feedback event requires running job'); END`,
			},
		},
		{
			name: "unsupported feedback route",
			statements: []string{
				"PRAGMA foreign_keys=OFF",
				"DROP TABLE feedback_events",
				`CREATE TABLE feedback_events (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					job_id INTEGER NOT NULL UNIQUE REFERENCES projects(id),
					document_id TEXT NOT NULL REFERENCES documents(id), invocation_id TEXT NOT NULL,
					correct INTEGER NOT NULL, score INTEGER NOT NULL, state TEXT NOT NULL,
					input_version_id INTEGER NOT NULL REFERENCES versions(id),
					result_version_id INTEGER NOT NULL REFERENCES versions(id),
					global_result_version_id INTEGER REFERENCES versions(id), created_at TEXT NOT NULL
				)`,
				`CREATE TRIGGER feedback_events_insert_guard BEFORE INSERT ON feedback_events
				WHEN NOT EXISTS (SELECT 1 FROM projects WHERE id=NEW.job_id)
				BEGIN SELECT RAISE(ABORT, 'feedback event requires running job'); END`,
				`CREATE TRIGGER feedback_events_immutable_update BEFORE UPDATE ON feedback_events
				BEGIN SELECT RAISE(ABORT, 'feedback events are append-only'); END`,
				`CREATE TRIGGER feedback_events_immutable_delete BEFORE DELETE ON feedback_events
				BEGIN SELECT RAISE(ABORT, 'feedback events are append-only'); END`,
				"PRAGMA foreign_keys=ON",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectionFixture(t)
			mutateProjectionDatabase(t, fixture.source, test.statements...)
			_, err := CreateProjection(context.Background(), fixture.request("invalid-schema"))
			assertProjectionCode(t, err, ProjectionSourceInvalid)
		})
	}
}

func TestValidateProjectionTableDefinitionRejectsWeakenedConstraint(t *testing.T) {
	canonical, err := canonicalProjectionDefinition("table", "jobs")
	if err != nil {
		t.Fatal(err)
	}
	weakened := strings.Replace(canonical, "dedupe_key TEXT UNIQUE", "dedupe_key TEXT", 1)
	if weakened == canonical {
		t.Fatal("test did not weaken the canonical jobs table")
	}
	if err := validateProjectionTableDefinition("jobs", weakened, canonical); err == nil {
		t.Fatal("weakened jobs table was accepted")
	}
}

func TestCreateProjectionRejectsCaseChangedStateLiteral(t *testing.T) {
	for _, name := range []string{"jobs_state_machine", "feedback_events_insert_guard"} {
		t.Run(name, func(t *testing.T) {
			fixture := newProjectionFixture(t)
			canonical, err := canonicalProjectionDefinition("trigger", name)
			if err != nil {
				t.Fatal(err)
			}
			weakened := strings.Replace(canonical, "'RUNNING'", "'running'", 1)
			if weakened == canonical {
				t.Fatal("test did not change the state literal")
			}
			mutateProjectionDatabase(t, fixture.source, "DROP TRIGGER "+name, weakened)
			_, err = CreateProjection(context.Background(), fixture.request("case-changed-state"))
			assertProjectionCode(t, err, ProjectionSourceInvalid)
		})
	}
}

func TestCreateProjectionAcceptsExpandedHistoricalFeedbackRoute(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "service", "knowledge.sqlite3")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	database := openHistoricalV4Database(t, source)
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	service, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	request := ProjectionRequest{
		KnowledgeRoot:  root,
		SourcePath:     source,
		ProjectionRoot: filepath.Join(root, "handoffs", "projections"),
		OperationID:    "historical-feedback-route",
	}
	receipt, err := CreateProjection(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "VERIFIED" {
		t.Fatalf("projection status = %q", receipt.Status)
	}
}

func mutateProjectionDatabase(t *testing.T, path string, statements ...string) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("mutate projection database: %v", err)
		}
	}
	if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	assertSQLiteSidecarsAbsent(t, path)
}
