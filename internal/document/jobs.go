package document

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var IntakeStages = []string{"inspect", "parse", "extract", "ocr", "chunk", "assess", "persist"}

type Job struct {
	ID             string         `json:"id"`
	State          string         `json:"state"`
	Stage          string         `json:"stage"`
	Progress       int            `json:"progress"`
	PageCount      *int           `json:"page_count"`
	Revision       int            `json:"revision"`
	Attempt        int            `json:"attempt"`
	MaxAttempts    int            `json:"max_attempts"`
	Owner          string         `json:"owner,omitempty"`
	LeaseExpiresAt float64        `json:"lease_expires_at,omitempty"`
	Payload        map[string]any `json:"payload"`
	Config         map[string]any `json:"config"`
	IdempotencyKey string         `json:"idempotency_key"`
	Result         map[string]any `json:"result,omitempty"`
	ErrorCategory  string         `json:"error_category,omitempty"`
	ErrorRef       string         `json:"error_ref,omitempty"`
	CreatedAt      float64        `json:"created_at"`
	UpdatedAt      float64        `json:"updated_at"`
}

type JobStore struct {
	database     *sql.DB
	owner        string
	leaseSeconds float64
	maxAttempts  int
	clock        func() time.Time
}

var (
	ErrLeaseLost       = errors.New("document intake lease lost")
	ErrInvalidJobState = errors.New("invalid document intake job transition")
)

func OpenJobStore(path string, lease time.Duration, maxAttempts int) (*JobStore, error) {
	return openJobStore(path, lease, maxAttempts, time.Now)
}

func openJobStore(path string, lease time.Duration, maxAttempts int, clock func() time.Time) (*JobStore, error) {
	if lease <= 0 || lease > time.Hour || maxAttempts < 1 || maxAttempts > 100 || clock == nil {
		return nil, errors.New("invalid document intake store options")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(absolute)+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(4)
	store := &JobStore{database: database, owner: randomIdentifier(), leaseSeconds: lease.Seconds(), maxAttempts: maxAttempts, clock: clock}
	if err := store.initialize(); err != nil {
		database.Close()
		return nil, err
	}
	if _, err := store.RecoverExpired(context.Background()); err != nil {
		database.Close()
		return nil, err
	}
	return store, nil
}

func (s *JobStore) Close() error { return s.database.Close() }

func (s *JobStore) initialize() error {
	if _, err := s.database.Exec(documentJobSchema); err != nil {
		return err
	}
	return nil
}

func (s *JobStore) Enqueue(ctx context.Context, payload, config map[string]any) (Job, error) {
	payloadJSON, err := strictJSON(payload, 64*1024)
	if err != nil {
		return Job{}, fmt.Errorf("invalid intake payload: %w", err)
	}
	configJSON, err := strictJSON(config, 32*1024)
	if err != nil {
		return Job{}, fmt.Errorf("invalid intake config: %w", err)
	}
	digest := sha256.Sum256(append(append([]byte{}, payloadJSON...), configJSON...))
	key := hex.EncodeToString(digest[:])
	now := float64(s.clock().UTC().UnixNano()) / 1e9
	identifier := randomIdentifier()
	_, err = s.database.ExecContext(ctx, `INSERT OR IGNORE INTO document_intake_jobs
(id,state,stage,progress,revision,attempt,max_attempts,payload,config,idempotency_key,created_at,updated_at)
VALUES (?,'QUEUED',?,0,0,0,?,?,?,?,?,?)`, identifier, IntakeStages[0], s.maxAttempts, string(payloadJSON), string(configJSON), key, now, now)
	if err != nil {
		return Job{}, err
	}
	var actual string
	if err := s.database.QueryRowContext(ctx, "SELECT id FROM document_intake_jobs WHERE idempotency_key=?", key).Scan(&actual); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, actual)
}

func (s *JobStore) Claim(ctx context.Context) (Job, bool, error) {
	return s.claim(ctx, "")
}

func (s *JobStore) ClaimID(ctx context.Context, identifier string) (Job, bool, error) {
	if strings.TrimSpace(identifier) == "" {
		return Job{}, false, errors.New("document intake job id is required")
	}
	return s.claim(ctx, identifier)
}

func (s *JobStore) claim(ctx context.Context, requestedID string) (Job, bool, error) {
	now := float64(s.clock().UTC().UnixNano()) / 1e9
	lease := now + s.leaseSeconds
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	var identifier string
	if requestedID == "" {
		err = tx.QueryRowContext(ctx, `SELECT id FROM document_intake_jobs
WHERE state='QUEUED' AND attempt < max_attempts ORDER BY created_at,id LIMIT 1`).Scan(&identifier)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT id FROM document_intake_jobs
WHERE id=? AND state='QUEUED' AND attempt < max_attempts`, requestedID).Scan(&identifier)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_intake_jobs SET
state='RUNNING',owner=?,lease_expires_at=?,heartbeat_at=?,attempt=attempt+1,revision=revision+1,updated_at=?
WHERE id=? AND state='QUEUED'`, s.owner, lease, now, now, identifier)
	if err != nil {
		return Job{}, false, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return Job{}, false, nil
	}
	if err := appendJobEvent(ctx, tx, identifier, "RUNNING", IntakeStages[0], 0, now); err != nil {
		return Job{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	job, err := s.Get(ctx, identifier)
	return job, err == nil, err
}

func (s *JobStore) Advance(ctx context.Context, identifier string, attempt int, stage string, pageCount int) (Job, error) {
	stageIndex := intakeStageIndex(stage)
	if stageIndex < 0 || pageCount < 0 || pageCount > 10000 {
		return Job{}, errors.New("invalid document intake progress")
	}
	current, err := s.Get(ctx, identifier)
	if err != nil {
		return Job{}, err
	}
	currentIndex := intakeStageIndex(current.Stage)
	if current.State != "RUNNING" || current.Owner != s.owner || current.Attempt != attempt || stageIndex < currentIndex || stageIndex > currentIndex+1 {
		return Job{}, ErrInvalidJobState
	}
	progress := stageProgress(stageIndex, pageCount)
	if progress < current.Progress {
		return Job{}, ErrInvalidJobState
	}
	now := float64(s.clock().UTC().UnixNano()) / 1e9
	lease := now + s.leaseSeconds
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE document_intake_jobs SET stage=?,progress=?,page_count=?,heartbeat_at=?,lease_expires_at=?,revision=revision+1,updated_at=?
WHERE id=? AND state='RUNNING' AND owner=? AND attempt=? AND lease_expires_at>?`, stage, progress, nullablePageCount(pageCount), now, lease, now, identifier, s.owner, attempt, now)
	if err != nil {
		return Job{}, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return Job{}, ErrLeaseLost
	}
	if err := appendJobEvent(ctx, tx, identifier, "RUNNING", stage, progress, now); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, identifier)
}

func (s *JobStore) Succeed(ctx context.Context, identifier string, attempt int, result map[string]any) (Job, error) {
	resultJSON, err := strictJSON(result, 1024*1024)
	if err != nil {
		return Job{}, err
	}
	now := float64(s.clock().UTC().UnixNano()) / 1e9
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	update, err := tx.ExecContext(ctx, `UPDATE document_intake_jobs SET state='SUCCEEDED',stage=?,progress=100,result=?,owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,revision=revision+1,updated_at=?
WHERE id=? AND state='RUNNING' AND owner=? AND attempt=? AND lease_expires_at>?`, IntakeStages[len(IntakeStages)-1], string(resultJSON), now, identifier, s.owner, attempt, now)
	if err != nil {
		return Job{}, err
	}
	rows, _ := update.RowsAffected()
	if rows != 1 {
		return Job{}, ErrLeaseLost
	}
	if err := appendJobEvent(ctx, tx, identifier, "SUCCEEDED", IntakeStages[len(IntakeStages)-1], 100, now); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, identifier)
}

func (s *JobStore) Fail(ctx context.Context, identifier string, attempt int, category string, detail error) (Job, error) {
	category = strings.ToUpper(strings.TrimSpace(category))
	if category == "" || len(category) > 64 || detail == nil {
		return Job{}, errors.New("invalid intake failure")
	}
	reference := sha256.Sum256([]byte(fmt.Sprintf("%T:%v", detail, detail)))
	now := float64(s.clock().UTC().UnixNano()) / 1e9
	result, err := s.database.ExecContext(ctx, `UPDATE document_intake_jobs SET state='FAILED',error_category=?,error_ref=?,owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,revision=revision+1,updated_at=?
WHERE id=? AND state='RUNNING' AND owner=? AND attempt=?`, category, hex.EncodeToString(reference[:]), now, identifier, s.owner, attempt)
	if err != nil {
		return Job{}, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return Job{}, ErrLeaseLost
	}
	return s.Get(ctx, identifier)
}

func (s *JobStore) Retry(ctx context.Context, identifier string) (Job, error) {
	now := float64(s.clock().UTC().UnixNano()) / 1e9
	result, err := s.database.ExecContext(ctx, `UPDATE document_intake_jobs SET state='QUEUED',error_category=NULL,error_ref=NULL,revision=revision+1,updated_at=?
WHERE id=? AND state='FAILED' AND attempt < max_attempts AND error_category NOT IN ('SECURITY','UNSAFE_SOURCE','UNSUPPORTED_FORMAT','INVALID_DOCUMENT')`, now, identifier)
	if err != nil {
		return Job{}, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return Job{}, ErrInvalidJobState
	}
	return s.Get(ctx, identifier)
}

func (s *JobStore) Cancel(ctx context.Context, identifier string) (Job, error) {
	now := float64(s.clock().UTC().UnixNano()) / 1e9
	result, err := s.database.ExecContext(ctx, `UPDATE document_intake_jobs SET state='CANCELLED',owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,revision=revision+1,updated_at=?
WHERE id=? AND state IN ('QUEUED','RUNNING') AND (owner IS NULL OR owner=?)`, now, identifier, s.owner)
	if err != nil {
		return Job{}, err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return Job{}, ErrInvalidJobState
	}
	return s.Get(ctx, identifier)
}

func (s *JobStore) RecoverExpired(ctx context.Context) (int64, error) {
	now := float64(s.clock().UTC().UnixNano()) / 1e9
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	requeued, err := tx.ExecContext(ctx, `UPDATE document_intake_jobs SET state='QUEUED',owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,revision=revision+1,updated_at=?
WHERE state='RUNNING' AND lease_expires_at<=? AND attempt < max_attempts`, now, now)
	if err != nil {
		return 0, err
	}
	reference := sha256.Sum256([]byte("document intake worker lease exhausted"))
	failed, err := tx.ExecContext(ctx, `UPDATE document_intake_jobs SET state='FAILED',error_category='WORKER_LOST',error_ref=?,owner=NULL,lease_expires_at=NULL,heartbeat_at=NULL,revision=revision+1,updated_at=?
WHERE state='RUNNING' AND lease_expires_at<=? AND attempt >= max_attempts`, hex.EncodeToString(reference[:]), now, now)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	requeuedCount, err := requeued.RowsAffected()
	if err != nil {
		return 0, err
	}
	failedCount, err := failed.RowsAffected()
	return requeuedCount + failedCount, err
}

func (s *JobStore) Get(ctx context.Context, identifier string) (Job, error) {
	row := s.database.QueryRowContext(ctx, `SELECT id,state,stage,progress,page_count,revision,attempt,max_attempts,COALESCE(owner,''),COALESCE(lease_expires_at,0),payload,config,idempotency_key,COALESCE(result,''),COALESCE(error_category,''),COALESCE(error_ref,''),created_at,updated_at
FROM document_intake_jobs WHERE id=?`, identifier)
	return scanJob(row)
}

func (s *JobStore) List(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	rows, err := s.database.QueryContext(ctx, `SELECT id,state,stage,progress,page_count,revision,attempt,max_attempts,COALESCE(owner,''),COALESCE(lease_expires_at,0),payload,config,idempotency_key,COALESCE(result,''),COALESCE(error_category,''),COALESCE(error_ref,''),created_at,updated_at
FROM document_intake_jobs ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanJob(row rowScanner) (Job, error) {
	var job Job
	var payload, config, result string
	var pageCount sql.NullInt64
	err := row.Scan(&job.ID, &job.State, &job.Stage, &job.Progress, &pageCount, &job.Revision, &job.Attempt, &job.MaxAttempts, &job.Owner, &job.LeaseExpiresAt, &payload, &config, &job.IdempotencyKey, &result, &job.ErrorCategory, &job.ErrorRef, &job.CreatedAt, &job.UpdatedAt)
	if err != nil {
		return Job{}, err
	}
	if pageCount.Valid {
		value := int(pageCount.Int64)
		job.PageCount = &value
	}
	if err := json.Unmarshal([]byte(payload), &job.Payload); err != nil {
		return Job{}, errors.New("stored intake payload is invalid")
	}
	if err := json.Unmarshal([]byte(config), &job.Config); err != nil {
		return Job{}, errors.New("stored intake config is invalid")
	}
	if result != "" && json.Unmarshal([]byte(result), &job.Result) != nil {
		return Job{}, errors.New("stored intake result is invalid")
	}
	if intakeStageIndex(job.Stage) < 0 || job.Progress < 0 || job.Progress > 100 || job.PageCount != nil && (*job.PageCount < 1 || *job.PageCount > 10000) || math.IsNaN(job.LeaseExpiresAt) || math.IsInf(job.LeaseExpiresAt, 0) {
		return Job{}, errors.New("stored intake job violates its contract")
	}
	return job, nil
}

func strictJSON(value any, limit int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > limit {
		return nil, errors.New("strict JSON value is invalid or too large")
	}
	return encoded, nil
}

func intakeStageIndex(stage string) int {
	for index, candidate := range IntakeStages {
		if stage == candidate {
			return index
		}
	}
	return -1
}

func stageProgress(stageIndex, pageCount int) int {
	base := stageIndex * 100 / len(IntakeStages)
	if pageCount > 0 && stageIndex > 0 && stageIndex < len(IntakeStages)-1 {
		base += min(12, int(math.Log2(float64(pageCount+1))))
	}
	return min(base, 99)
}

func nullablePageCount(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func appendJobEvent(ctx context.Context, tx *sql.Tx, identifier, state, stage string, progress int, now float64) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO document_intake_job_events(job_id,state,stage,progress,created_at) VALUES (?,?,?,?,?)", identifier, state, stage, progress, now)
	return err
}

func randomIdentifier() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

const documentJobSchema = `
CREATE TABLE IF NOT EXISTS document_intake_jobs (
 id TEXT PRIMARY KEY,
 state TEXT NOT NULL CHECK(state IN ('QUEUED','RUNNING','SUCCEEDED','FAILED','CANCELLED')),
 stage TEXT NOT NULL CHECK(stage IN ('inspect','parse','extract','ocr','chunk','assess','persist')),
 progress INTEGER NOT NULL CHECK(progress BETWEEN 0 AND 100),
 revision INTEGER NOT NULL CHECK(revision>=0),
 attempt INTEGER NOT NULL CHECK(attempt>=0),
 max_attempts INTEGER NOT NULL CHECK(max_attempts BETWEEN 1 AND 100),
 owner TEXT, lease_expires_at REAL, heartbeat_at REAL, page_count INTEGER,
 payload TEXT NOT NULL, config TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE,
 result TEXT, error_category TEXT, error_ref TEXT,
 created_at REAL NOT NULL, updated_at REAL NOT NULL,
 CHECK((state='RUNNING' AND owner IS NOT NULL AND lease_expires_at IS NOT NULL AND heartbeat_at IS NOT NULL)
    OR (state!='RUNNING' AND owner IS NULL AND lease_expires_at IS NULL AND heartbeat_at IS NULL)),
 CHECK((error_category IS NULL)=(error_ref IS NULL)),
 CHECK(state!='SUCCEEDED' OR progress=100),
 CHECK(state='SUCCEEDED' OR progress<100)
);
CREATE TABLE IF NOT EXISTS document_intake_job_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 job_id TEXT NOT NULL REFERENCES document_intake_jobs(id),
 state TEXT NOT NULL, stage TEXT NOT NULL, progress INTEGER NOT NULL, created_at REAL NOT NULL
);
CREATE TRIGGER IF NOT EXISTS document_intake_job_events_no_update BEFORE UPDATE ON document_intake_job_events
BEGIN SELECT RAISE(ABORT,'document intake events are append-only'); END;
CREATE TRIGGER IF NOT EXISTS document_intake_job_events_no_delete BEFORE DELETE ON document_intake_job_events
BEGIN SELECT RAISE(ABORT,'document intake events are append-only'); END;
`
