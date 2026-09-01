package knowledge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const jobLeaseDuration = 30 * time.Second

var errJobLeaseLost = errors.New("writer job lease lost")

type jobLease struct {
	owner     string
	attempt   int
	expiresAt string
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

func (s *Service) Job(identifier int64) (Job, error) {
	table, rawIdentifier, err := decodeJobIdentifier(identifier)
	if err != nil {
		return Job{}, err
	}
	var job Job
	var storedIdentifier int64
	var errorText sql.NullString
	err = s.database.QueryRow(
		"SELECT id, kind, payload, state, error, attempt FROM "+table+" WHERE id = ?",
		rawIdentifier,
	).Scan(&storedIdentifier, &job.Kind, &job.Payload, &job.State, &errorText, &job.Attempt)
	if err != nil {
		return Job{}, err
	}
	job.ID = encodeJobIdentifier(table, storedIdentifier)
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
	table := s.jobTableForKind(kind)
	var result sql.Result
	if key == "" {
		result, err = s.database.Exec("INSERT INTO "+table+"(kind, payload, state, created_at) VALUES (?, ?, 'QUEUED', ?)", kind, string(encoded), now)
	} else {
		result, err = s.database.Exec("INSERT OR IGNORE INTO "+table+"(kind, payload, state, dedupe_key, created_at) VALUES (?, ?, 'QUEUED', ?, ?)", kind, string(encoded), key, now)
	}
	if err != nil {
		return 0, err
	}
	rawJobID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if rowsAffected == 0 && key != "" {
		if err := s.database.QueryRow("SELECT id FROM "+table+" WHERE dedupe_key = ?", key).Scan(&rawJobID); err != nil {
			return 0, err
		}
	}
	jobID := encodeJobIdentifier(table, rawJobID)
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
	table, rawJobID, err := decodeJobIdentifier(jobID)
	if err != nil {
		return err
	}
	lease, err := s.claimJob(table, rawJobID, time.Now().UTC())
	if err != nil {
		return err
	}
	return s.executeClaimedJob(table, rawJobID, lease)
}

func (s *Service) claimJob(table string, rawJobID int64, claimedAt time.Time) (jobLease, error) {
	now := claimedAt.Format(time.RFC3339Nano)
	lease := jobLease{
		owner:     s.owner,
		expiresAt: claimedAt.Add(jobLeaseDuration).Format(time.RFC3339Nano),
	}
	err := s.database.QueryRow("UPDATE "+table+` SET state='RUNNING', owner=?, heartbeat_at=?, lease_expires_at=?, started_at=?, attempt=attempt+1
WHERE id=? AND state='QUEUED' RETURNING attempt`, lease.owner, now, lease.expiresAt, now, rawJobID).Scan(&lease.attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return jobLease{}, errors.New("writer job could not be claimed")
	}
	if err != nil {
		return jobLease{}, err
	}
	return lease, nil
}

func (s *Service) executeClaimedJob(table string, rawJobID int64, lease jobLease) error {
	tx, err := s.database.Begin()
	if err != nil {
		return s.failJob(table, rawJobID, lease, err)
	}
	var kind, encoded string
	err = tx.QueryRow("SELECT kind, payload FROM "+table+` WHERE id=? AND state='RUNNING'
AND owner=? AND attempt=? AND lease_expires_at=?
AND julianday(lease_expires_at)>julianday('now')`,
		rawJobID, lease.owner, lease.attempt, lease.expiresAt,
	).Scan(&kind, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return rollbackJob(tx, errJobLeaseLost)
	}
	if err != nil {
		return s.failJob(table, rawJobID, lease, rollbackJob(tx, err))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(encoded), &payload); err != nil {
		return s.failJob(table, rawJobID, lease, rollbackJob(tx, err))
	}
	if err := validateOperation(kind, payload); err != nil {
		return s.failJob(table, rawJobID, lease, rollbackJob(tx, err))
	}
	now := timestamp()
	if err := applyOperation(tx, rawJobID, kind, payload, now); err != nil {
		return s.failJob(table, rawJobID, lease, rollbackJob(tx, err))
	}
	result, err := tx.Exec("UPDATE "+table+` SET state='SUCCEEDED', finished_at=?
WHERE id=? AND state='RUNNING' AND owner=? AND attempt=? AND lease_expires_at=?
AND julianday(lease_expires_at)>julianday('now')`,
		timestamp(), rawJobID, lease.owner, lease.attempt, lease.expiresAt,
	)
	if err != nil {
		return s.failJob(table, rawJobID, lease, rollbackJob(tx, err))
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return s.failJob(table, rawJobID, lease, rollbackJob(tx, err))
	}
	if rows != 1 {
		return rollbackJob(tx, errJobLeaseLost)
	}
	return tx.Commit()
}

func rollbackJob(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("%w (also failed to roll back job transaction: %v)", cause, err)
	}
	return cause
}

func (s *Service) failJob(table string, rawJobID int64, lease jobLease, cause error) error {
	message := fmt.Sprintf("%T: %v", cause, cause)
	if len(message) > 1024 {
		message = message[:1024]
	}
	result, err := s.database.Exec("UPDATE "+table+` SET state='FAILED', error=?, finished_at=?
WHERE id=? AND state='RUNNING' AND owner=? AND attempt=? AND lease_expires_at=?
AND julianday(lease_expires_at)>julianday('now')`,
		message, timestamp(), rawJobID, lease.owner, lease.attempt, lease.expiresAt,
	)
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
