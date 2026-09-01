package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type projectionTableContract struct {
	name    string
	columns []string
}

type projectionTriggerContract struct {
	name  string
	table string
}

var projectionRequiredTables = []projectionTableContract{
	{"projects", []string{"id", "name", "scope", "alias", "created_at"}},
	{"originals", []string{"sha256", "content", "created_at"}},
	{"documents", []string{"id", "project_id", "title", "deleted_at"}},
	{"versions", []string{"id", "document_id", "ordinal", "state", "original_sha256", "created_at"}},
	{"chunks", []string{"id", "version_id", "ordinal", "content"}},
	{"sources", []string{"id", "version_id", "kind", "locator"}},
	{"governance", []string{"id", "version_id", "action", "actor", "created_at"}},
	{"audit", []string{"id", "event", "subject_id", "created_at", "detail"}},
	{"jobs", []string{
		"id", "kind", "payload", "state", "dedupe_key", "error", "created_at",
		"started_at", "finished_at", "owner", "lease_expires_at", "heartbeat_at", "attempt",
	}},
	{"snapshots", []string{"id", "project_id", "generation", "state", "created_at"}},
	{"snapshot_versions", []string{"snapshot_id", "document_id", "version_id"}},
	{"active_snapshots", []string{"project_id", "snapshot_id"}},
	{"import_documents", []string{"project_id", "content_sha256", "document_id", "version_id"}},
	{"import_provenance", []string{
		"document_id", "source_kind", "source_ref", "source_sha256", "scanner", "scan_state", "governance_state",
	}},
	{"import_receipts", []string{"marker", "project_id", "receipt", "receipt_sha256", "completed_at"}},
	{"feedback_jobs", []string{
		"id", "kind", "payload", "state", "dedupe_key", "error", "created_at",
		"started_at", "finished_at", "owner", "lease_expires_at", "heartbeat_at", "attempt",
	}},
	{"feedback_events", []string{
		"id", "job_id", "document_id", "invocation_id", "correct", "score", "state",
		"input_version_id", "result_version_id", "global_result_version_id", "created_at",
	}},
	{"global_sync", []string{"source_document_id", "global_document_id", "created_at"}},
}

var projectionRequiredTriggers = []projectionTriggerContract{
	{"projects_immutable", "projects"},
	{"documents_soft_delete_candidate", "documents"},
	{"originals_immutable_update", "originals"},
	{"originals_immutable_delete", "originals"},
	{"versions_append_only", "versions"},
	{"versions_no_delete", "versions"},
	{"versions_state_machine", "versions"},
	{"audit_immutable_update", "audit"},
	{"audit_immutable_delete", "audit"},
	{"jobs_insert_guard", "jobs"},
	{"jobs_payload_immutable", "jobs"},
	{"jobs_state_machine", "jobs"},
	{"jobs_lease_guard", "jobs"},
	{"snapshots_insert_guard", "snapshots"},
	{"snapshots_immutable", "snapshots"},
	{"snapshots_no_delete", "snapshots"},
	{"snapshot_versions_insert_guard", "snapshot_versions"},
	{"snapshot_versions_immutable", "snapshot_versions"},
	{"snapshot_versions_no_delete", "snapshot_versions"},
	{"active_snapshots_insert_guard", "active_snapshots"},
	{"active_snapshots_update_guard", "active_snapshots"},
	{"active_snapshots_no_delete", "active_snapshots"},
	{"feedback_jobs_insert_guard", "feedback_jobs"},
	{"feedback_jobs_payload_immutable", "feedback_jobs"},
	{"feedback_jobs_state_machine", "feedback_jobs"},
	{"feedback_jobs_lease_guard", "feedback_jobs"},
	{"feedback_events_insert_guard", "feedback_events"},
	{"feedback_events_immutable_update", "feedback_events"},
	{"feedback_events_immutable_delete", "feedback_events"},
	{"global_sync_immutable_update", "global_sync"},
	{"global_sync_immutable_delete", "global_sync"},
	{"global_sync_insert_guard", "global_sync"},
}

func validateProjectionSchema(ctx context.Context, database *sql.DB) error {
	for _, contract := range projectionRequiredTables {
		if err := validateProjectionTable(ctx, database, contract); err != nil {
			return err
		}
	}
	if err := validateProjectionIndex(ctx, database, "projects_scope_alias", "projects"); err != nil {
		return err
	}
	triggerSQL := ""
	for _, contract := range projectionRequiredTriggers {
		definition, err := projectionObjectDefinition(ctx, database, "trigger", contract.name, contract.table)
		if err != nil {
			return err
		}
		if contract.name == "feedback_events_insert_guard" {
			triggerSQL = definition
			continue
		}
		canonical, err := canonicalProjectionDefinition("trigger", contract.name)
		if err != nil {
			return err
		}
		if normalizeProjectionSQL(definition) != normalizeProjectionSQL(canonical) {
			return fmt.Errorf("required trigger %s does not enforce the canonical invariant", contract.name)
		}
	}
	return validateProjectionFeedbackRoute(ctx, database, triggerSQL)
}

func validateProjectionTable(ctx context.Context, database *sql.DB, contract projectionTableContract) error {
	definition, err := projectionObjectDefinition(ctx, database, "table", contract.name, contract.name)
	if err != nil {
		return err
	}
	canonical, err := canonicalProjectionDefinition("table", contract.name)
	if err != nil {
		return err
	}
	if err := validateProjectionTableDefinition(contract.name, definition, canonical); err != nil {
		return err
	}
	rows, err := database.QueryContext(ctx, "PRAGMA table_info("+contract.name+")")
	if err != nil {
		return fmt.Errorf("inspect required table %s: %w", contract.name, err)
	}
	defer rows.Close()
	columns := make(map[string]bool, len(contract.columns))
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("inspect required table %s: %w", contract.name, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect required table %s: %w", contract.name, err)
	}
	for _, column := range contract.columns {
		if !columns[column] {
			return fmt.Errorf("required table %s is missing column %s", contract.name, column)
		}
	}
	return nil
}

func validateProjectionIndex(ctx context.Context, database *sql.DB, name, table string) error {
	definition, err := projectionObjectDefinition(ctx, database, "index", name, table)
	if err != nil {
		return err
	}
	canonical, err := canonicalProjectionDefinition("unique index", name)
	if err != nil {
		return err
	}
	if normalizeProjectionSQL(definition) != normalizeProjectionSQL(canonical) {
		return fmt.Errorf("required index %s does not enforce canonical uniqueness", name)
	}
	return nil
}

func projectionObjectDefinition(
	ctx context.Context,
	database *sql.DB,
	kind string,
	name string,
	table string,
) (string, error) {
	var definition string
	err := database.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_master WHERE type=? AND name=? AND tbl_name=?", kind, name, table,
	).Scan(&definition)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("required %s %s on %s is missing", kind, name, table)
	}
	if err != nil {
		return "", fmt.Errorf("inspect required %s %s: %w", kind, name, err)
	}
	if strings.TrimSpace(definition) == "" {
		return "", fmt.Errorf("required %s %s has no definition", kind, name)
	}
	return definition, nil
}

type projectionForeignKey struct {
	id, sequence          int
	table, source, target string
}

func validateProjectionFeedbackRoute(ctx context.Context, database *sql.DB, guardSQL string) error {
	rows, err := database.QueryContext(ctx, "PRAGMA foreign_key_list(feedback_events)")
	if err != nil {
		return fmt.Errorf("inspect feedback route: %w", err)
	}
	defer rows.Close()
	var foreignKeys []projectionForeignKey
	for rows.Next() {
		var key projectionForeignKey
		var onUpdate, onDelete, match string
		if err := rows.Scan(
			&key.id, &key.sequence, &key.table, &key.source, &key.target,
			&onUpdate, &onDelete, &match,
		); err != nil {
			return fmt.Errorf("inspect feedback route: %w", err)
		}
		foreignKeys = append(foreignKeys, key)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect feedback route: %w", err)
	}
	var route *projectionForeignKey
	for index := range foreignKeys {
		if foreignKeys[index].source != "job_id" {
			continue
		}
		if route != nil {
			return errors.New("feedback job foreign key is ambiguous")
		}
		route = &foreignKeys[index]
	}
	if route == nil || route.sequence != 0 || route.target != "id" ||
		(route.table != jobsTable && route.table != feedbackJobsTable) {
		return errors.New("feedback job foreign key is invalid")
	}
	for _, key := range foreignKeys {
		if key.id == route.id && key.source != "job_id" {
			return errors.New("feedback job foreign key must be single-column")
		}
	}
	normalizedGuard := normalizeProjectionSQL(guardSQL)
	whenAt := strings.Index(normalizedGuard, "when")
	beginAt := strings.Index(normalizedGuard, "begin")
	if whenAt < 0 || beginAt <= whenAt {
		return errors.New("feedback event guard has no canonical predicate")
	}
	predicate := normalizedGuard[whenAt+len("when") : beginAt]
	expected := "notexists(select1from" + route.table +
		"whereid=new.job_idandkind='record_feedback'andstate='RUNNING')"
	if predicate != expected {
		return errors.New("feedback event guard does not match foreign key route, kind, and state")
	}
	return nil
}

func canonicalProjectionDefinition(kind, name string) (string, error) {
	prefix := "create" + strings.ReplaceAll(kind, " ", "") + name
	for _, statements := range [][]string{schemaV1Statements, schemaV2Statements, schemaV3Statements, schemaV4Statements} {
		for _, statement := range statements {
			if strings.HasPrefix(normalizeProjectionSQL(statement), prefix) {
				return statement, nil
			}
		}
	}
	return "", fmt.Errorf("canonical %s %s is missing", kind, name)
}

func normalizeProjectionSQL(statement string) string {
	var builder strings.Builder
	builder.Grow(len(statement))
	inLiteral := false
	for index := 0; index < len(statement); index++ {
		character := statement[index]
		if character == '\'' {
			builder.WriteByte(character)
			if inLiteral && index+1 < len(statement) && statement[index+1] == '\'' {
				builder.WriteByte(statement[index+1])
				index++
				continue
			}
			inLiteral = !inLiteral
			continue
		}
		if !inLiteral && (character == ' ' || character == '\t' || character == '\r' || character == '\n') {
			continue
		}
		if !inLiteral && character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		builder.WriteByte(character)
	}
	normalized := builder.String()
	for _, kind := range []string{"table", "uniqueindex", "trigger"} {
		normalized = strings.Replace(normalized, "create"+kind+"ifnotexists", "create"+kind, 1)
	}
	return normalized
}

func validateProjectionTableDefinition(name, definition, canonical string) error {
	actualClauses, err := projectionTableClauses(definition)
	if err != nil {
		return fmt.Errorf("inspect required table %s: %w", name, err)
	}
	requiredClauses, err := projectionTableClauses(canonical)
	if err != nil {
		return fmt.Errorf("inspect canonical table %s: %w", name, err)
	}
	actual := make(map[string]bool, len(actualClauses))
	for _, clause := range actualClauses {
		actual[clause] = true
	}
	for _, required := range requiredClauses {
		if actual[required] {
			continue
		}
		if name == "feedback_events" && strings.Contains(required, "referencesfeedback_jobs(id)") &&
			actual[strings.Replace(required, "referencesfeedback_jobs(id)", "referencesjobs(id)", 1)] {
			continue
		}
		if name == "jobs" && strings.HasPrefix(required, "kindtextnotnullcheck(kindin(") &&
			strings.HasSuffix(required, "))") &&
			actual[strings.TrimSuffix(required, "))")+",'record_feedback'))"] {
			continue
		}
		return fmt.Errorf("required table %s is missing canonical clause %s", name, required)
	}
	return nil
}

func projectionTableClauses(statement string) ([]string, error) {
	normalized := normalizeProjectionSQL(statement)
	openAt := strings.IndexByte(normalized, '(')
	closeAt := strings.LastIndexByte(normalized, ')')
	if openAt < 0 || closeAt <= openAt {
		return nil, errors.New("table definition has no body")
	}
	body := normalized[openAt+1 : closeAt]
	depth := 0
	start := 0
	clauses := make([]string, 0)
	for index, character := range body {
		switch character {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				clauses = append(clauses, body[start:index])
				start = index + 1
			}
		}
		if depth < 0 {
			return nil, errors.New("table definition has unbalanced parentheses")
		}
	}
	if depth != 0 {
		return nil, errors.New("table definition has unbalanced parentheses")
	}
	clauses = append(clauses, body[start:])
	return clauses, nil
}
