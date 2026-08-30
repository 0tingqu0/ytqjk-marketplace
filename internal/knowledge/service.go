package knowledge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type Service struct {
	database *sql.DB
	path     string
	writer   sync.Mutex
	owner    string
}

type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	Alias     string `json:"alias"`
	CreatedAt string `json:"created_at"`
}

type Version struct {
	ID             int64  `json:"id"`
	DocumentID     string `json:"document_id"`
	Ordinal        int    `json:"ordinal"`
	State          string `json:"state"`
	OriginalSHA256 string `json:"original_sha256"`
	CreatedAt      string `json:"created_at"`
}

type Snapshot struct {
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	Generation int    `json:"generation"`
	State      string `json:"state"`
	CreatedAt  string `json:"created_at"`
}

type SnapshotMember struct {
	DocumentID string `json:"document_id"`
	VersionID  int64  `json:"version_id"`
}

type ActiveSnapshot struct {
	Snapshot Snapshot         `json:"snapshot"`
	Versions []SnapshotMember `json:"versions"`
}

type Job struct {
	ID                int64  `json:"id"`
	Kind              string `json:"kind"`
	Payload           string `json:"payload"`
	State             string `json:"state"`
	Error             string `json:"error,omitempty"`
	Attempt           int    `json:"attempt"`
	PayloadDocumentID string `json:"payload_document_id,omitempty"`
}

func Open(path string) (*Service, error) {
	database, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	owner, err := newUUID()
	if err != nil {
		database.Close()
		return nil, err
	}
	return &Service{database: database, path: path, owner: owner}, nil
}

func (s *Service) Close() error { return s.database.Close() }

func (s *Service) SchemaVersion() (int, error) {
	var version int
	err := s.database.QueryRow("PRAGMA user_version").Scan(&version)
	return version, err
}

func (s *Service) CreateProject(scope, alias string) (string, error) {
	if err := validateText(scope, 256); err != nil {
		return "", err
	}
	if err := validateText(alias, 1024); err != nil {
		return "", err
	}
	identifier, err := newUUID()
	if err != nil {
		return "", err
	}
	payload := map[string]any{"id": identifier, "scope": strings.TrimSpace(scope), "alias": strings.TrimSpace(alias)}
	key := dedupeKey("project", map[string]any{"scope": payload["scope"], "alias": payload["alias"]})
	if _, err := s.submit("create_project", payload, key); err != nil {
		return "", err
	}
	var existing string
	err = s.database.QueryRow("SELECT id FROM projects WHERE scope = ? AND alias = ?", payload["scope"], payload["alias"]).Scan(&existing)
	return existing, err
}

func (s *Service) CreateCandidate(projectID, title, content, source string) (string, error) {
	if !validUUID(projectID) {
		return "", errors.New("project identifier must be UUID")
	}
	for _, item := range []struct {
		value string
		limit int
	}{{title, 4096}, {content, 16 * 1024 * 1024}, {source, 16384}} {
		if err := validateText(item.value, item.limit); err != nil {
			return "", err
		}
	}
	documentID, err := newUUID()
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"document_id": documentID, "project_id": projectID, "title": strings.TrimSpace(title),
		"content": content, "source": strings.TrimSpace(source),
	}
	keyPayload := cloneMap(payload)
	delete(keyPayload, "document_id")
	jobID, err := s.submit("create_candidate", payload, dedupeKey("candidate", keyPayload))
	if err != nil {
		return "", err
	}
	job, err := s.Job(jobID)
	if err != nil {
		return "", err
	}
	return job.PayloadDocumentID, nil
}

func (s *Service) EditCandidate(documentID, content, source string) error {
	if !validUUID(documentID) {
		return errors.New("document identifier must be UUID")
	}
	if err := validateText(content, 16*1024*1024); err != nil {
		return err
	}
	if err := validateText(source, 16384); err != nil {
		return err
	}
	_, err := s.submit("edit_candidate", map[string]any{"document_id": documentID, "content": content, "source": source}, "")
	return err
}

func (s *Service) SoftDeleteCandidate(documentID string) error {
	if !validUUID(documentID) {
		return errors.New("document identifier must be UUID")
	}
	_, err := s.submit("soft_delete_candidate", map[string]any{"document_id": documentID}, "")
	return err
}

func (s *Service) AppendState(documentID, state string, content *string) error {
	if !validUUID(documentID) {
		return errors.New("document identifier must be UUID")
	}
	state = strings.ToLower(strings.TrimSpace(state))
	if state != "approved" && state != "verified" && state != "tombstone" {
		return errors.New("append state is invalid")
	}
	if content != nil {
		if err := validateText(*content, 16*1024*1024); err != nil {
			return err
		}
	}
	_, err := s.submit("append_state", map[string]any{"document_id": documentID, "state": state, "content": content}, "")
	return err
}

func (s *Service) CreateSnapshot(projectID string) (string, error) {
	if !validUUID(projectID) {
		return "", errors.New("project identifier must be UUID")
	}
	identifier, err := newUUID()
	if err != nil {
		return "", err
	}
	_, err = s.submit("create_snapshot", map[string]any{"project_id": projectID, "snapshot_id": identifier}, "")
	return identifier, err
}

func (s *Service) RecordFeedback(documentID, invocationID string, correct bool) error {
	if !validUUID(documentID) || !validUUID(invocationID) {
		return errors.New("feedback identifiers must be UUID")
	}
	payload := map[string]any{"document_id": documentID, "invocation_id": invocationID, "correct": correct}
	_, err := s.submit("record_feedback", payload, dedupeKey("feedback", payload))
	return err
}

func (s *Service) RecycleBin(projectID string) ([]map[string]any, error) {
	if !validUUID(projectID) {
		return nil, errors.New("project identifier must be UUID")
	}
	rows, err := s.database.Query(`SELECT d.id,d.title,v.id,v.created_at FROM documents d
JOIN versions v ON v.document_id=d.id WHERE d.project_id=? AND d.deleted_at IS NULL
AND v.ordinal=(SELECT MAX(latest.ordinal) FROM versions latest WHERE latest.document_id=d.id)
AND v.state='tombstone' ORDER BY v.created_at,d.id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []map[string]any
	for rows.Next() {
		var identifier, title, createdAt string
		var versionID int64
		if err := rows.Scan(&identifier, &title, &versionID, &createdAt); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"id": identifier, "title": title, "version_id": versionID, "created_at": createdAt})
	}
	return result, rows.Err()
}

func (s *Service) Project(identifier string) (Project, error) {
	var project Project
	err := s.database.QueryRow("SELECT id, name, scope, alias, created_at FROM projects WHERE id = ?", identifier).
		Scan(&project.ID, &project.Name, &project.Scope, &project.Alias, &project.CreatedAt)
	return project, err
}

func (s *Service) Projects() ([]Project, error) {
	rows, err := s.database.Query("SELECT id, name, scope, alias, created_at FROM projects ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Project
	for rows.Next() {
		var project Project
		if err := rows.Scan(&project.ID, &project.Name, &project.Scope, &project.Alias, &project.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, project)
	}
	return result, rows.Err()
}

func (s *Service) DocumentVersions(documentID string) ([]Version, error) {
	rows, err := s.database.Query("SELECT id, document_id, ordinal, state, original_sha256, created_at FROM versions WHERE document_id = ? ORDER BY ordinal", documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Version
	for rows.Next() {
		var version Version
		if err := rows.Scan(&version.ID, &version.DocumentID, &version.Ordinal, &version.State, &version.OriginalSHA256, &version.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, version)
	}
	return result, rows.Err()
}

func (s *Service) ActiveSnapshot(projectID string) (*ActiveSnapshot, error) {
	var result ActiveSnapshot
	err := s.database.QueryRow(`SELECT s.id, s.project_id, s.generation, s.state, s.created_at
FROM active_snapshots a JOIN snapshots s ON s.id = a.snapshot_id WHERE a.project_id = ?`, projectID).
		Scan(&result.Snapshot.ID, &result.Snapshot.ProjectID, &result.Snapshot.Generation, &result.Snapshot.State, &result.Snapshot.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.database.Query("SELECT document_id, version_id FROM snapshot_versions WHERE snapshot_id = ? ORDER BY document_id", result.Snapshot.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var member SnapshotMember
		if err := rows.Scan(&member.DocumentID, &member.VersionID); err != nil {
			return nil, err
		}
		result.Versions = append(result.Versions, member)
	}
	return &result, rows.Err()
}

func (s *Service) Count(table string) (int, error) {
	if !validTable(table) {
		return 0, errors.New("unsupported table")
	}
	var count int
	err := s.database.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
	return count, err
}

func (s *Service) Job(identifier int64) (Job, error) {
	var job Job
	var errorText sql.NullString
	err := s.database.QueryRow("SELECT id, kind, payload, state, error, attempt FROM jobs WHERE id = ?", identifier).
		Scan(&job.ID, &job.Kind, &job.Payload, &job.State, &errorText, &job.Attempt)
	if err != nil {
		return Job{}, err
	}
	job.Error = errorText.String
	var payload map[string]any
	if json.Unmarshal([]byte(job.Payload), &payload) == nil {
		job.PayloadDocumentID, _ = payload["document_id"].(string)
	}
	return job, nil
}

func (s *Service) submit(kind string, payload map[string]any, key string) (int64, error) {
	if err := validateOperation(kind, payload); err != nil {
		return 0, err
	}
	s.writer.Lock()
	defer s.writer.Unlock()
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	now := timestamp()
	var result sql.Result
	if key == "" {
		result, err = s.database.Exec("INSERT INTO jobs(kind, payload, state, created_at) VALUES (?, ?, 'QUEUED', ?)", kind, string(encoded), now)
	} else {
		result, err = s.database.Exec("INSERT OR IGNORE INTO jobs(kind, payload, state, dedupe_key, created_at) VALUES (?, ?, 'QUEUED', ?, ?)", kind, string(encoded), key, now)
	}
	if err != nil {
		return 0, err
	}
	jobID, _ := result.LastInsertId()
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 && key != "" {
		if err := s.database.QueryRow("SELECT id FROM jobs WHERE dedupe_key = ?", key).Scan(&jobID); err != nil {
			return 0, err
		}
	}
	for attempt := 0; attempt < 100; attempt++ {
		job, err := s.Job(jobID)
		if err != nil {
			return 0, err
		}
		switch job.State {
		case "SUCCEEDED":
			return jobID, nil
		case "FAILED":
			return 0, errors.New(job.Error)
		case "QUEUED":
			if err := s.executeJob(jobID); err != nil && !strings.Contains(err.Error(), "could not be claimed") {
				return 0, err
			}
		case "RUNNING":
			time.Sleep(10 * time.Millisecond)
		default:
			return 0, errors.New("writer job has invalid state")
		}
	}
	return 0, errors.New("writer job did not reach a terminal state")
}

func (s *Service) executeJob(jobID int64) error {
	ctx := context.Background()
	now := timestamp()
	lease := time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339Nano)
	result, err := s.database.Exec(`UPDATE jobs SET state='RUNNING', owner=?, heartbeat_at=?, lease_expires_at=?, started_at=?, attempt=attempt+1
WHERE id=? AND state='QUEUED'`, s.owner, now, lease, now, jobID)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return errors.New("writer job could not be claimed")
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return s.failJob(jobID, err)
	}
	var kind, encoded string
	if err := tx.QueryRow("SELECT kind, payload FROM jobs WHERE id = ?", jobID).Scan(&kind, &encoded); err != nil {
		tx.Rollback()
		return s.failJob(jobID, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		tx.Rollback()
		return s.failJob(jobID, err)
	}
	if err := validateOperation(kind, payload); err != nil {
		tx.Rollback()
		return s.failJob(jobID, err)
	}
	if err := applyOperation(tx, jobID, kind, payload, now); err != nil {
		tx.Rollback()
		return s.failJob(jobID, err)
	}
	if _, err := tx.Exec("UPDATE jobs SET state='SUCCEEDED', finished_at=? WHERE id=? AND state='RUNNING' AND owner=?", timestamp(), jobID, s.owner); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Service) failJob(jobID int64, cause error) error {
	message := fmt.Sprintf("%T: %v", cause, cause)
	if len(message) > 1024 {
		message = message[:1024]
	}
	result, err := s.database.Exec("UPDATE jobs SET state='FAILED', error=?, finished_at=? WHERE id=? AND state='RUNNING' AND owner=?", message, timestamp(), jobID, s.owner)
	if err != nil {
		return fmt.Errorf("%w (also failed to persist job failure: %v)", cause, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w (also failed to inspect job failure update: %v)", cause, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w (job failure state was not persisted)", cause)
	}
	return cause
}

func validateOperation(kind string, payload map[string]any) error {
	required := map[string][]string{
		"create_project":        {"id", "scope", "alias"},
		"create_candidate":      {"document_id", "project_id", "title", "content", "source"},
		"edit_candidate":        {"document_id", "content", "source"},
		"soft_delete_candidate": {"document_id"},
		"append_state":          {"document_id", "state", "content"},
		"create_snapshot":       {"project_id", "snapshot_id"},
		"record_feedback":       {"document_id", "invocation_id", "correct"},
	}
	fields, ok := required[kind]
	if !ok || len(fields) != len(payload) {
		return errors.New("operation payload fields are invalid")
	}
	for _, field := range fields {
		if _, ok := payload[field]; !ok {
			return errors.New("operation payload fields are invalid")
		}
		if strings.HasSuffix(field, "id") {
			value, ok := payload[field].(string)
			if !ok || !validUUID(value) {
				return errors.New("identifier must be UUID")
			}
		}
	}
	return nil
}

func applyOperation(tx *sql.Tx, jobID int64, kind string, payload map[string]any, now string) error {
	switch kind {
	case "create_project":
		_, err := tx.Exec("INSERT INTO projects(id, name, scope, alias, created_at) VALUES (?, ?, ?, ?, ?)", payload["id"], payload["alias"], payload["scope"], payload["alias"], now)
		if err == nil {
			err = audit(tx, "project_created", payload["id"].(string), now)
		}
		return err
	case "create_candidate":
		if _, err := tx.Exec("INSERT INTO documents(id, project_id, title) VALUES (?, ?, ?)", payload["document_id"], payload["project_id"], payload["title"]); err != nil {
			return err
		}
		if _, err := appendVersion(tx, payload["document_id"].(string), "candidate", payload["content"].(string), payload["source"].(string), "local", now); err != nil {
			return err
		}
		return audit(tx, "candidate_created", payload["document_id"].(string), now)
	case "edit_candidate":
		if err := requireEditable(tx, payload["document_id"].(string)); err != nil {
			return err
		}
		if _, err := appendVersion(tx, payload["document_id"].(string), "candidate", payload["content"].(string), payload["source"].(string), "local", now); err != nil {
			return err
		}
		return audit(tx, "candidate_edited", payload["document_id"].(string), now)
	case "soft_delete_candidate":
		identifier := payload["document_id"].(string)
		if err := requireEditable(tx, identifier); err != nil {
			return err
		}
		if _, err := tx.Exec("UPDATE documents SET deleted_at=? WHERE id=?", now, identifier); err != nil {
			return err
		}
		return audit(tx, "candidate_soft_deleted", identifier, now)
	case "append_state":
		return appendState(tx, payload, now)
	case "create_snapshot":
		return createSnapshot(tx, payload["project_id"].(string), payload["snapshot_id"].(string), now)
	case "record_feedback":
		return recordFeedback(tx, jobID, payload, now)
	default:
		return errors.New("unsupported operation")
	}
}

func appendVersion(tx *sql.Tx, documentID, state, content, source, sourceKind, now string) (int64, error) {
	digest := sha256.Sum256([]byte(content))
	digestText := hex.EncodeToString(digest[:])
	if _, err := tx.Exec("INSERT OR IGNORE INTO originals(sha256, content, created_at) VALUES (?, ?, ?)", digestText, []byte(content), now); err != nil {
		return 0, err
	}
	var ordinal int
	if err := tx.QueryRow("SELECT COALESCE(MAX(ordinal),0)+1 FROM versions WHERE document_id=?", documentID).Scan(&ordinal); err != nil {
		return 0, err
	}
	result, err := tx.Exec("INSERT INTO versions(document_id, ordinal, state, original_sha256, created_at) VALUES (?, ?, ?, ?, ?)", documentID, ordinal, state, digestText, now)
	if err != nil {
		return 0, err
	}
	versionID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	chunkDigest := sha256.Sum256([]byte(fmt.Sprintf("%d:1:%s", versionID, digestText)))
	if _, err := tx.Exec("INSERT INTO chunks(id, version_id, ordinal, content) VALUES (?, ?, 1, ?)", hex.EncodeToString(chunkDigest[:]), versionID, content); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("INSERT INTO sources(version_id, kind, locator) VALUES (?, ?, ?)", versionID, sourceKind, source); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("INSERT INTO governance(version_id, action, actor, created_at) VALUES (?, ?, ?, ?)", versionID, state, "一听曲就困", now); err != nil {
		return 0, err
	}
	return versionID, nil
}

func requireEditable(tx *sql.Tx, documentID string) error {
	var mirrored int
	if err := tx.QueryRow("SELECT COUNT(*) FROM global_sync WHERE global_document_id=?", documentID).Scan(&mirrored); err != nil {
		return err
	}
	if mirrored != 0 {
		return errors.New("system-managed global mirrors are immutable")
	}
	var deleted sql.NullString
	var state string
	err := tx.QueryRow(`SELECT d.deleted_at, v.state FROM documents d JOIN versions v ON v.document_id=d.id
WHERE d.id=? ORDER BY v.ordinal DESC LIMIT 1`, documentID).Scan(&deleted, &state)
	if err != nil || deleted.Valid || state != "candidate" {
		return errors.New("only active candidate revisions are editable")
	}
	return nil
}

func appendState(tx *sql.Tx, payload map[string]any, now string) error {
	documentID := payload["document_id"].(string)
	state := payload["state"].(string)
	var mirrored int
	if err := tx.QueryRow("SELECT COUNT(*) FROM global_sync WHERE global_document_id=?", documentID).Scan(&mirrored); err != nil {
		return err
	}
	if mirrored != 0 {
		return errors.New("system-managed global mirrors are immutable")
	}
	var prior, digest string
	if err := tx.QueryRow("SELECT state, original_sha256 FROM versions WHERE document_id=? ORDER BY ordinal DESC LIMIT 1", documentID).Scan(&prior, &digest); err != nil {
		return err
	}
	allowed := map[string]map[string]bool{
		"candidate": {"candidate": true, "approved": true, "verified": true, "tombstone": true},
		"approved":  {"verified": true, "tombstone": true}, "verified": {"tombstone": true},
	}
	if !allowed[prior][state] {
		return errors.New("invalid governance state transition")
	}
	content := ""
	if value, ok := payload["content"].(string); ok && value != "" {
		content = value
	} else if err := tx.QueryRow("SELECT CAST(content AS TEXT) FROM originals WHERE sha256=?", digest).Scan(&content); err != nil {
		return err
	}
	if _, err := appendVersion(tx, documentID, state, content, "governance", "local", now); err != nil {
		return err
	}
	return audit(tx, "version_"+state, documentID, now)
}

func createSnapshot(tx *sql.Tx, projectID, snapshotID, now string) error {
	var generation int
	if err := tx.QueryRow("SELECT COALESCE(MAX(generation),0)+1 FROM snapshots WHERE project_id=?", projectID).Scan(&generation); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO snapshots(id, project_id, generation, state, created_at) VALUES (?, ?, ?, 'BUILDING', ?)", snapshotID, projectID, generation, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO snapshot_versions(snapshot_id, document_id, version_id)
SELECT ?, d.id, v.id FROM documents d JOIN versions v ON v.document_id=d.id
WHERE d.project_id=? AND d.deleted_at IS NULL
AND v.ordinal=(SELECT MAX(latest.ordinal) FROM versions latest WHERE latest.document_id=d.id)
AND v.state!='tombstone'`, snapshotID, projectID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE snapshots SET state='ACTIVE' WHERE id=?", snapshotID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO active_snapshots(project_id, snapshot_id) VALUES (?, ?)
ON CONFLICT(project_id) DO UPDATE SET snapshot_id=excluded.snapshot_id`, projectID, snapshotID); err != nil {
		return err
	}
	return audit(tx, "snapshot_activated", snapshotID, now)
}

func audit(tx *sql.Tx, event, subjectID, now string) error {
	_, err := tx.Exec("INSERT INTO audit(event, subject_id, created_at, detail) VALUES (?, ?, ?, '{}')", event, subjectID, now)
	return err
}

func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func newUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func validUUID(value string) bool { return uuidPattern.MatchString(value) }

func validateText(value string, limit int) error {
	if strings.TrimSpace(value) == "" || len(value) > limit {
		return errors.New("text field is required or exceeds limit")
	}
	return nil
}

func dedupeKey(kind string, payload map[string]any) string {
	encoded, _ := json.Marshal(map[string]any{"kind": kind, "payload": payload})
	digest := sha256.Sum256(encoded)
	return kind + ":" + hex.EncodeToString(digest[:])
}

func cloneMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
