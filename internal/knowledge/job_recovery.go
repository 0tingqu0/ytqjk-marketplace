package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type runningJobLease struct {
	table                   string
	identifier              int64
	owner, heartbeat, lease sql.NullString
}

func recoverStaleJobLeases(ctx context.Context, connection *sql.Conn) error {
	jobs, err := readRunningJobLeases(ctx, connection)
	if err != nil {
		return err
	}
	reference := time.Now().UTC()
	stale := make([]runningJobLease, 0, len(jobs))
	liveCount := 0
	for _, job := range jobs {
		if !job.owner.Valid || !job.heartbeat.Valid || !job.lease.Valid {
			stale = append(stale, job)
			continue
		}
		lease, err := time.Parse(time.RFC3339Nano, job.lease.String)
		if err != nil {
			return fmt.Errorf("%s job %d has an invalid RUNNING lease: %w", job.table, job.identifier, err)
		}
		if lease.After(reference) {
			liveCount++
			continue
		}
		stale = append(stale, job)
	}
	if liveCount > 1 {
		return fmt.Errorf("knowledge database has %d live RUNNING job leases", liveCount)
	}
	for _, job := range stale {
		result, err := connection.ExecContext(
			ctx,
			"UPDATE "+job.table+" SET state='QUEUED',owner=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=? AND state='RUNNING'",
			job.identifier,
		)
		if err != nil {
			return fmt.Errorf("recover expired %s job %d: %w", job.table, job.identifier, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("inspect recovered %s job %d: %w", job.table, job.identifier, err)
		}
		if rows != 1 {
			return fmt.Errorf("recover expired %s job %d: row changed", job.table, job.identifier)
		}
	}
	return nil
}

func readRunningJobLeases(ctx context.Context, connection *sql.Conn) ([]runningJobLease, error) {
	var result []runningJobLease
	for _, table := range []string{jobsTable, feedbackJobsTable} {
		rows, err := connection.QueryContext(
			ctx,
			"SELECT id,owner,heartbeat_at,lease_expires_at FROM "+table+" WHERE state='RUNNING' ORDER BY id",
		)
		if err != nil {
			return nil, fmt.Errorf("read %s RUNNING leases: %w", table, err)
		}
		for rows.Next() {
			job := runningJobLease{table: table}
			if err := rows.Scan(&job.identifier, &job.owner, &job.heartbeat, &job.lease); err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, job)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}
