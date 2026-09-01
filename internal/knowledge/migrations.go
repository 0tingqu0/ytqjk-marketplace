package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type migrationStep struct {
	version    int
	statements []string
	repair     func(*sql.Conn) error
}

var schemaMigrations = []migrationStep{
	{version: 1, statements: schemaV1Statements, repair: repairJobsSchema},
	{version: 2, statements: schemaV2Statements},
	{version: 3, statements: schemaV3Statements, repair: repairLegacyImportSchema},
	{version: 4, statements: schemaV4Statements},
}

func migrate(database *sql.DB) (string, error) {
	ctx := context.Background()
	connection, err := database.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(ctx, "ROLLBACK")
		}
	}()
	var currentVersion int
	if err := connection.QueryRowContext(ctx, "PRAGMA user_version").Scan(&currentVersion); err != nil {
		return "", err
	}
	if currentVersion > LatestSchema {
		return "", fmt.Errorf("knowledge schema %d is newer than supported schema %d", currentVersion, LatestSchema)
	}

	for _, step := range schemaMigrations {
		if err := applyMigrationStep(ctx, connection, step); err != nil {
			return "", err
		}
		if currentVersion < step.version {
			if _, err := connection.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(step.version)); err != nil {
				return "", fmt.Errorf("set knowledge schema v%d: %w", step.version, err)
			}
		}
	}
	if err := ensureJobStateMachineTriggers(ctx, connection); err != nil {
		return "", fmt.Errorf("repair knowledge job state machines: %w", err)
	}
	if err := recoverStaleJobLeases(ctx, connection); err != nil {
		return "", fmt.Errorf("recover knowledge job leases: %w", err)
	}
	feedbackJobs, err := ensureFeedbackEventRoute(ctx, connection)
	if err != nil {
		return "", fmt.Errorf("validate feedback job route: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return "", err
	}
	committed = true
	return feedbackJobs, nil
}

func ensureFeedbackEventRoute(ctx context.Context, connection *sql.Conn) (string, error) {
	route, err := detectFeedbackJobsTableOn(ctx, connection)
	if err != nil {
		return "", err
	}
	var guardExists int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
WHERE type='trigger' AND name='feedback_events_insert_guard'`).Scan(&guardExists); err != nil {
		return "", err
	}
	if guardExists != 0 {
		if err := probeFeedbackEventRoute(ctx, connection, route); err == nil {
			return route, nil
		}
		if _, err := connection.ExecContext(ctx, "DROP TRIGGER feedback_events_insert_guard"); err != nil {
			return "", err
		}
	}
	if err := createFeedbackEventGuard(ctx, connection, route); err != nil {
		return "", err
	}
	if err := probeFeedbackEventRoute(ctx, connection, route); err != nil {
		return "", err
	}
	return route, nil
}

func createFeedbackEventGuard(ctx context.Context, connection *sql.Conn, route string) error {
	statement := fmt.Sprintf(`CREATE TRIGGER feedback_events_insert_guard
BEFORE INSERT ON feedback_events WHEN NOT EXISTS (
  SELECT 1 FROM %s WHERE id=NEW.job_id AND kind='record_feedback' AND state='RUNNING')
BEGIN SELECT RAISE(ABORT, 'feedback event requires running job'); END`, route)
	_, err := connection.ExecContext(ctx, statement)
	return err
}

func probeFeedbackEventRoute(
	ctx context.Context,
	connection *sql.Conn,
	route string,
) (returnedErr error) {
	if _, err := connection.ExecContext(ctx, "SAVEPOINT feedback_route_probe"); err != nil {
		return err
	}
	defer func() {
		_, rollbackErr := connection.ExecContext(ctx, "ROLLBACK TO feedback_route_probe")
		_, releaseErr := connection.ExecContext(ctx, "RELEASE feedback_route_probe")
		returnedErr = errors.Join(returnedErr, rollbackErr, releaseErr)
	}()
	projectID, err := newUUID()
	if err != nil {
		return err
	}
	documentID, err := newUUID()
	if err != nil {
		return err
	}
	validInvocation, err := newUUID()
	if err != nil {
		return err
	}
	invalidInvocation, err := newUUID()
	if err != nil {
		return err
	}
	nonFeedbackInvocation, err := newUUID()
	if err != nil {
		return err
	}
	originalSHA := strings.TrimPrefix(
		dedupeKey("feedback-route-probe", map[string]any{"project_id": projectID}),
		"feedback-route-probe:",
	)
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO projects(id,name,scope,alias,created_at) VALUES (?,?,?,?,?)",
			[]any{projectID, "feedback route probe", "project", "probe-" + projectID, "probe"}},
		{"INSERT INTO originals(sha256,content,created_at) VALUES (?,?,?)",
			[]any{originalSHA, "probe", "probe"}},
		{"INSERT INTO documents(id,project_id,title) VALUES (?,?,?)",
			[]any{documentID, projectID, "feedback route probe"}},
	}
	for _, statement := range statements {
		if _, err := connection.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	versionResult, err := connection.ExecContext(ctx, `INSERT INTO versions(
document_id,ordinal,state,original_sha256,created_at) VALUES (?,1,'candidate',?,'probe')`,
		documentID, originalSHA)
	if err != nil {
		return err
	}
	versionID, err := versionResult.LastInsertId()
	if err != nil {
		return err
	}
	var validJobID int64
	if err := connection.QueryRowContext(ctx, `SELECT MIN(
COALESCE((SELECT MIN(id) FROM jobs),0),
COALESCE((SELECT MIN(id) FROM feedback_jobs),0),0)-1`).Scan(&validJobID); err != nil {
		return err
	}
	invalidJobID := validJobID - 1
	for _, job := range []struct {
		id  int64
		key string
	}{{validJobID, "valid-" + projectID}, {invalidJobID, "invalid-" + projectID}} {
		query := "INSERT INTO " + route +
			"(id,kind,payload,state,dedupe_key,created_at) VALUES (?,'record_feedback','{}','QUEUED',?,'probe')"
		if _, err := connection.ExecContext(ctx, query, job.id, job.key); err != nil {
			return fmt.Errorf("feedback job table does not accept record_feedback: %w", err)
		}
	}
	if _, err := connection.ExecContext(ctx, "UPDATE "+route+` SET state='RUNNING',
owner='probe',lease_expires_at='2999-01-01T00:00:00Z',heartbeat_at='probe',attempt=attempt+1
WHERE id=?`, validJobID); err != nil {
		return err
	}
	insertEvent := `INSERT INTO feedback_events(job_id,document_id,invocation_id,correct,score,state,
input_version_id,result_version_id,created_at) VALUES (?,?,?,1,1,'candidate',?,?,'probe')`
	if _, err := connection.ExecContext(
		ctx, insertEvent, validJobID, documentID, validInvocation, versionID, versionID,
	); err != nil {
		return fmt.Errorf("feedback event guard rejected a running job: %w", err)
	}
	if _, err := connection.ExecContext(
		ctx, insertEvent, invalidJobID, documentID, invalidInvocation, versionID, versionID,
	); err == nil {
		return errors.New("feedback event guard accepted a queued job")
	}
	if route == jobsTable {
		nonFeedbackJobID := invalidJobID - 1
		if _, err := connection.ExecContext(ctx, `INSERT INTO jobs(
id,kind,payload,state,dedupe_key,created_at)
VALUES (?,'create_project','{}','QUEUED',?,'probe')`,
			nonFeedbackJobID, "non-feedback-"+projectID); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, `UPDATE jobs SET state='RUNNING',
owner='probe',lease_expires_at='2999-01-01T00:00:00Z',heartbeat_at='probe',attempt=attempt+1
WHERE id=?`, nonFeedbackJobID); err != nil {
			return err
		}
		if _, err := connection.ExecContext(
			ctx, insertEvent, nonFeedbackJobID, documentID, nonFeedbackInvocation, versionID, versionID,
		); err == nil {
			return errors.New("feedback event guard accepted a non-feedback job")
		}
	}
	return nil
}

func ensureJobStateMachineTriggers(ctx context.Context, connection *sql.Conn) error {
	triggers := []struct {
		name       string
		statements []string
	}{
		{name: "jobs_state_machine", statements: schemaV1Statements},
		{name: "feedback_jobs_state_machine", statements: schemaV4Statements},
	}
	for _, trigger := range triggers {
		var existing string
		if err := connection.QueryRowContext(
			ctx, "SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?", trigger.name,
		).Scan(&existing); err != nil {
			return err
		}
		if strings.Contains(existing, "OLD.lease_expires_at IS NULL") &&
			strings.Contains(existing, "OLD.heartbeat_at IS NULL") {
			continue
		}
		canonical := ""
		prefix := "CREATE TRIGGER IF NOT EXISTS " + trigger.name + " "
		for _, statement := range trigger.statements {
			if strings.HasPrefix(statement, prefix) {
				canonical = statement
				break
			}
		}
		if canonical == "" {
			return fmt.Errorf("canonical trigger %s is missing", trigger.name)
		}
		if _, err := connection.ExecContext(ctx, "DROP TRIGGER "+trigger.name); err != nil {
			return err
		}
		if _, err := connection.ExecContext(ctx, canonical); err != nil {
			return err
		}
	}
	return nil
}

func applyMigrationStep(ctx context.Context, connection *sql.Conn, step migrationStep) error {
	for _, statement := range step.statements {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("knowledge schema v%d migration: %w", step.version, err)
		}
	}
	if step.repair != nil {
		if err := step.repair(connection); err != nil {
			return fmt.Errorf("knowledge schema v%d additive repair: %w", step.version, err)
		}
	}
	return nil
}

func repairJobsSchema(connection *sql.Conn) error {
	exists, err := objectExists(connection, "table", "jobs")
	if err != nil || !exists {
		return err
	}
	columns, err := connectionColumns(connection, "jobs")
	if err != nil {
		return err
	}
	optional := []struct{ name, definition string }{
		{"dedupe_key", "TEXT"},
		{"error", "TEXT"},
		{"started_at", "TEXT"},
		{"finished_at", "TEXT"},
		{"owner", "TEXT"},
		{"lease_expires_at", "TEXT"},
		{"heartbeat_at", "TEXT"},
		{"attempt", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, item := range optional {
		if columns[item.name] {
			continue
		}
		statement := "ALTER TABLE jobs ADD COLUMN " + item.name + " " + item.definition
		if _, err := connection.ExecContext(context.Background(), statement); err != nil {
			return fmt.Errorf("add jobs.%s: %w", item.name, err)
		}
	}
	return nil
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
		return fmt.Errorf("add import_provenance.governance_state: %w", err)
	}
	return nil
}

func objectExists(connection *sql.Conn, kind, name string) (bool, error) {
	var count int
	err := connection.QueryRowContext(
		context.Background(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type=? AND name=?",
		kind,
		name,
	).Scan(&count)
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
