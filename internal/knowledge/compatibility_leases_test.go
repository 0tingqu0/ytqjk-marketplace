package knowledge

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenRejectsUnsafeRunningLeasesWithoutPartialRecovery(t *testing.T) {
	tests := []struct {
		name      string
		wantError string
		blockers  func(*testing.T, *sql.DB) []jobLeaseRef
	}{
		{
			name:      "invalid",
			wantError: "invalid RUNNING lease",
			blockers: func(t *testing.T, database *sql.DB) []jobLeaseRef {
				identifier := insertRunningLease(t, database, feedbackJobsTable, "record_feedback", "invalid", "not-a-timestamp")
				return []jobLeaseRef{{table: feedbackJobsTable, identifier: identifier}}
			},
		},
		{
			name:      "two_live",
			wantError: "2 live RUNNING job leases",
			blockers: func(t *testing.T, database *sql.DB) []jobLeaseRef {
				jobsID := insertRunningLease(t, database, jobsTable, "create_project", "live-jobs", futureLease())
				feedbackID := insertRunningLease(t, database, feedbackJobsTable, "record_feedback", "live-feedback", futureLease())
				return []jobLeaseRef{
					{table: jobsTable, identifier: jobsID},
					{table: feedbackJobsTable, identifier: feedbackID},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "unsafe-lease.sqlite3")
			service, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			staleID := insertRunningLease(t, service.database, jobsTable, "create_project", "stale", "2000-01-01T00:00:01Z")
			refs := append([]jobLeaseRef{{table: jobsTable, identifier: staleID}}, test.blockers(t, service.database)...)
			before := make([]jobLeaseSnapshot, len(refs))
			for index, ref := range refs {
				before[index] = readJobLeaseSnapshot(t, service.database, ref)
			}
			if err := service.Close(); err != nil {
				t.Fatal(err)
			}

			if reopened, err := Open(path); err == nil {
				reopened.Close()
				t.Fatal("Open accepted unsafe RUNNING leases")
			} else if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Open error = %v, want %q", err, test.wantError)
			}
			database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			for index, ref := range refs {
				if got := readJobLeaseSnapshot(t, database, ref); got != before[index] {
					t.Fatalf("%s job %d partially changed: got %#v want %#v", ref.table, ref.identifier, got, before[index])
				}
			}
		})
	}
}

type jobLeaseRef struct {
	table      string
	identifier int64
}

type jobLeaseSnapshot struct {
	state                            string
	owner, heartbeat, lease, started sql.NullString
	attempt                          int
}

func insertRunningLease(t *testing.T, database *sql.DB, table, kind, key, lease string) int64 {
	t.Helper()
	result, err := database.Exec(
		"INSERT INTO "+table+"(kind,payload,state,dedupe_key,created_at) VALUES (?, '{}', 'QUEUED', ?, 'created')",
		kind,
		key,
	)
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		"UPDATE "+table+" SET state='RUNNING',owner='owner',heartbeat_at='heartbeat',lease_expires_at=?,started_at='started',attempt=attempt+1 WHERE id=?",
		lease,
		identifier,
	); err != nil {
		t.Fatal(err)
	}
	return identifier
}

func readJobLeaseSnapshot(t *testing.T, database *sql.DB, ref jobLeaseRef) jobLeaseSnapshot {
	t.Helper()
	var result jobLeaseSnapshot
	if err := database.QueryRow(
		"SELECT state,owner,heartbeat_at,lease_expires_at,started_at,attempt FROM "+ref.table+" WHERE id=?",
		ref.identifier,
	).Scan(&result.state, &result.owner, &result.heartbeat, &result.lease, &result.started, &result.attempt); err != nil {
		t.Fatal(err)
	}
	return result
}

func futureLease() string {
	return time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
}
