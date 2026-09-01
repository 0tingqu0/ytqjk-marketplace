package document

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
)

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
