package knowledge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

const fencingProjectID = "11111111-1111-4111-8111-111111111111"

func TestExecuteClaimedJobRejectsReclaimedLease(t *testing.T) {
	service := openFencingTestService(t)
	rawJobID := insertQueuedProjectJob(t, service, fencingProjectID, "reclaimed-lease")
	staleLease, err := service.claimJob(jobsTable, rawJobID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	expireAndRequeueJob(t, service.database, rawJobID)
	service.owner = "worker-b"
	currentLease, err := service.claimJob(jobsTable, rawJobID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	err = service.executeClaimedJob(jobsTable, rawJobID, staleLease)
	if !errors.Is(err, errJobLeaseLost) {
		t.Fatalf("stale worker error = %v", err)
	}
	staleFailure := errors.New("stale worker operation failed")
	if err := service.failJob(jobsTable, rawJobID, staleLease, staleFailure); !errors.Is(err, staleFailure) {
		t.Fatalf("stale failure error = %v", err)
	}
	assertProjectAbsent(t, service.database, fencingProjectID)
	assertRunningLease(t, service.database, rawJobID, currentLease)
}

func TestExecuteClaimedJobRollsBackWhenCompletionFenceMisses(t *testing.T) {
	service := openFencingTestService(t)
	rawJobID := insertQueuedProjectJob(t, service, fencingProjectID, "completion-fence")
	lease, err := service.claimJob(jobsTable, rawJobID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	trigger := fmt.Sprintf(`CREATE TRIGGER force_job_reclaim AFTER INSERT ON projects
WHEN NEW.id='%s' BEGIN
  UPDATE jobs SET heartbeat_at='2000-01-01T00:00:00Z',lease_expires_at='2000-01-01T00:00:01Z' WHERE id=%d;
  UPDATE jobs SET state='QUEUED',owner=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=%d;
  UPDATE jobs SET state='RUNNING',owner='worker-b',heartbeat_at='2999-01-01T00:00:00Z',
    lease_expires_at='2999-01-01T00:00:01Z',attempt=attempt+1 WHERE id=%d;
END`, fencingProjectID, rawJobID, rawJobID, rawJobID)
	if _, err := service.database.Exec(trigger); err != nil {
		t.Fatal(err)
	}

	err = service.executeClaimedJob(jobsTable, rawJobID, lease)
	if !errors.Is(err, errJobLeaseLost) {
		t.Fatalf("completion fence error = %v", err)
	}
	assertProjectAbsent(t, service.database, fencingProjectID)
	assertRunningLease(t, service.database, rawJobID, lease)
}

func openFencingTestService(t *testing.T) *Service {
	t.Helper()
	service, err := Open(filepath.Join(t.TempDir(), "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("close service: %v", err)
		}
	})
	return service
}

func insertQueuedProjectJob(t *testing.T, service *Service, projectID, alias string) int64 {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"id": projectID, "scope": "project", "alias": alias})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.database.Exec(
		"INSERT INTO jobs(kind,payload,state,created_at) VALUES ('create_project',?,'QUEUED',?)",
		string(payload), timestamp(),
	)
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return identifier
}

func expireAndRequeueJob(t *testing.T, database *sql.DB, rawJobID int64) {
	t.Helper()
	if _, err := database.Exec(
		"UPDATE jobs SET heartbeat_at=?,lease_expires_at=? WHERE id=?",
		"2000-01-01T00:00:00Z", "2000-01-01T00:00:01Z", rawJobID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		"UPDATE jobs SET state='QUEUED',owner=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=?",
		rawJobID,
	); err != nil {
		t.Fatal(err)
	}
}

func assertProjectAbsent(t *testing.T, database *sql.DB, projectID string) {
	t.Helper()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM projects WHERE id=?", projectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale worker committed %d project rows", count)
	}
}

func assertRunningLease(t *testing.T, database *sql.DB, rawJobID int64, want jobLease) {
	t.Helper()
	var state, owner, expiresAt string
	var attempt int
	var jobError sql.NullString
	if err := database.QueryRow(
		"SELECT state,owner,attempt,lease_expires_at,error FROM jobs WHERE id=?", rawJobID,
	).Scan(&state, &owner, &attempt, &expiresAt, &jobError); err != nil {
		t.Fatal(err)
	}
	if state != "RUNNING" || owner != want.owner || attempt != want.attempt || expiresAt != want.expiresAt || jobError.Valid {
		t.Fatalf("job lease = state=%s owner=%s attempt=%d expires=%s error=%v, want %#v", state, owner, attempt, expiresAt, jobError, want)
	}
}
