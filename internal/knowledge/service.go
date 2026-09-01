package knowledge

import (
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
	database     *sql.DB
	path         string
	writer       sync.Mutex
	owner        string
	feedbackJobs string
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

func Open(path string) (*Service, error) {
	database, feedbackJobs, err := openDatabaseWithFeedbackRoute(path)
	if err != nil {
		return nil, err
	}
	owner, err := newUUID()
	if err != nil {
		_ = closeDatabase(database)
		return nil, err
	}
	return &Service{database: database, path: path, owner: owner, feedbackJobs: feedbackJobs}, nil
}

func (s *Service) Close() error {
	s.writer.Lock()
	defer s.writer.Unlock()
	return closeDatabase(s.database)
}

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
