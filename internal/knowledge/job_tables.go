package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const (
	jobsTable         = "jobs"
	feedbackJobsTable = "feedback_jobs"
)

func detectFeedbackJobsTable(database *sql.DB) (string, error) {
	return detectFeedbackJobsTableOn(context.Background(), database)
}

type feedbackRouteQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type feedbackForeignKey struct {
	identifier, sequence  int
	table, source, target string
}

func detectFeedbackJobsTableOn(ctx context.Context, queryer feedbackRouteQueryer) (string, error) {
	rows, err := queryer.QueryContext(ctx, "PRAGMA foreign_key_list(feedback_events)")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var keys []feedbackForeignKey
	for rows.Next() {
		var key feedbackForeignKey
		var onUpdate, onDelete, match string
		if err := rows.Scan(
			&key.identifier,
			&key.sequence,
			&key.table,
			&key.source,
			&key.target,
			&onUpdate,
			&onDelete,
			&match,
		); err != nil {
			return "", err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	var route *feedbackForeignKey
	for index := range keys {
		if keys[index].source != "job_id" {
			continue
		}
		if route != nil {
			return "", errors.New("feedback job foreign key is ambiguous")
		}
		route = &keys[index]
	}
	if route == nil {
		return "", errors.New("feedback job foreign key is missing")
	}
	if route.sequence != 0 || route.target != "id" ||
		(route.table != jobsTable && route.table != feedbackJobsTable) {
		return "", fmt.Errorf("feedback job foreign key route is invalid: %s.%s", route.table, route.target)
	}
	for _, key := range keys {
		if key.identifier == route.identifier && key.source != "job_id" {
			return "", errors.New("feedback job foreign key must be single-column")
		}
	}
	return route.table, nil
}

func (s *Service) jobTableForKind(kind string) string {
	if kind == "record_feedback" {
		return s.feedbackJobs
	}
	return jobsTable
}

func encodeJobIdentifier(table string, rawIdentifier int64) int64 {
	if table == feedbackJobsTable {
		return -rawIdentifier
	}
	return rawIdentifier
}

func decodeJobIdentifier(identifier int64) (string, int64, error) {
	if identifier > 0 {
		return jobsTable, identifier, nil
	}
	if identifier < 0 {
		return feedbackJobsTable, -identifier, nil
	}
	return "", 0, errors.New("job identifier must be non-zero")
}
